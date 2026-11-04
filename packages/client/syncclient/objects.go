package syncclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/decision"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	objectsv1 "github.com/xlsft/harness/packages/client/gen/harness/objects/v1"
	"github.com/xlsft/harness/packages/client/llm"
)

// Kinds of objects, as the queue and its callers name them.
const (
	ObjectAttachment = "attachment"
	ObjectImage      = "image"
	ObjectScratch    = "scratch"
	ObjectToolResult = "tool_result"
	ObjectSentFile   = "sent_file"
)

var objectKindProto = map[string]objectsv1.ObjectKind{
	ObjectAttachment: objectsv1.ObjectKind_OBJECT_KIND_ATTACHMENT,
	ObjectImage:      objectsv1.ObjectKind_OBJECT_KIND_IMAGE,
	ObjectScratch:    objectsv1.ObjectKind_OBJECT_KIND_SCRATCH,
	ObjectToolResult: objectsv1.ObjectKind_OBJECT_KIND_TOOL_RESULT,
	ObjectSentFile:   objectsv1.ObjectKind_OBJECT_KIND_SENT_FILE,
}

const (
	// DefaultScratchMaxBytes is the largest scratch or tool result file that is uploaded; larger ones stay local.
	DefaultScratchMaxBytes = 100 << 20
	// scratchSettle is how long a file must rest before it is uploaded, so a file being written goes up once.
	scratchSettle = 5 * time.Second
)

// ObjectsAPI is the part of ObjectsService the queue needs (implemented by *Client).
type ObjectsAPI interface {
	StartUpload(ctx context.Context, req *objectsv1.StartUploadRequest) (*objectsv1.StartUploadResponse, error)
	UploadChunk(ctx context.Context, req *objectsv1.UploadChunkRequest) (*objectsv1.UploadChunkResponse, error)
	FinishUpload(ctx context.Context, req *objectsv1.FinishUploadRequest) (*objectsv1.FinishUploadResponse, error)
	DeleteObject(ctx context.Context, req *objectsv1.DeleteObjectRequest) (*objectsv1.DeleteObjectResponse, error)
	GetObjectLimits(ctx context.Context, req *objectsv1.GetObjectLimitsRequest) (*objectsv1.GetObjectLimitsResponse, error)
}

// ObjectQueueOptions configure an ObjectQueue.
type ObjectQueueOptions struct {
	API ObjectsAPI
	// Dir holds the queue; default DefaultDir()/objects.
	Dir string
	// Interval is the retry and scan period of Run (default 30 s).
	Interval time.Duration
	// ScratchMaxBytes is DefaultScratchMaxBytes when 0.
	ScratchMaxBytes int64
	Logger          *slog.Logger
}

// ObjectQueue uploads a session's files to the server's object store in the background, next to the transcript
// uploader. What it has to send is on disk before anything refers to it, under Dir:
//
//	queue/<id>.json    one file per pending upload (survives restarts; the id makes a repeated add a no-op)
//	spool/<sha256>     bytes that exist nowhere else as files (images from a transcript, a file as it was sent)
//	scan/<session>.json the scratch directories being watched and what of them has gone up
//	server.json        whether the server has a store, as last heard
//
// Uploads are content-addressed, so a retry or a file another session already sent costs one StartUpload. Offline, or
// against a server without a store (an old one), everything stays queued and the session goes on with its local files.
// Methods are safe for concurrent use, and several processes may share Dir: the worst case is one upload twice.
type ObjectQueue struct {
	o   ObjectQueueOptions
	log *slog.Logger

	mu        sync.Mutex
	available bool
	probed    bool
	warned    map[string]bool

	wake    chan struct{}
	flushMu sync.Mutex // serializes uploads
	scanMu  sync.Mutex // guards the files under scan/ (opening a session must not wait for an upload)
}

// NewObjectQueue creates a queue. It starts believing what the server last said about having a store.
func NewObjectQueue(o ObjectQueueOptions) *ObjectQueue {
	if o.Dir == "" {
		o.Dir = filepath.Join(DefaultDir(), "objects")
	}
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.ScratchMaxBytes <= 0 {
		o.ScratchMaxBytes = DefaultScratchMaxBytes
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	q := &ObjectQueue{o: o, log: log, wake: make(chan struct{}, 1), warned: map[string]bool{}}
	if b, err := os.ReadFile(filepath.Join(o.Dir, "server.json")); err == nil {
		var s struct {
			Available bool `json:"available"`
		}
		if json.Unmarshal(b, &s) == nil {
			q.available = s.Available
		}
	}
	return q
}

var (
	sharedQueuesMu sync.Mutex
	sharedQueues   = map[string]*ObjectQueue{}
)

// SharedObjectQueue returns the process's queue, running until the process ends: a client serves many sessions, and
// their files all go through one queue. nil api returns nil.
func SharedObjectQueue(api ObjectsAPI, log *slog.Logger) *ObjectQueue {
	if api == nil {
		return nil
	}
	dir := filepath.Join(DefaultDir(), "objects")
	sharedQueuesMu.Lock()
	defer sharedQueuesMu.Unlock()
	if q := sharedQueues[dir]; q != nil {
		return q
	}
	q := NewObjectQueue(ObjectQueueOptions{API: api, Dir: dir, Logger: log})
	sharedQueues[dir] = q
	go func() { _ = q.Run(context.Background()) }()
	return q
}

// Available reports whether the server has a store, as last heard. False means: keep files local, as without a server.
func (q *ObjectQueue) Available() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.available
}

// Probe asks the server whether it has a store, once per queue while it answers. A network failure changes nothing.
func (q *ObjectQueue) Probe(ctx context.Context) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	done, avail := q.probed, q.available
	q.mu.Unlock()
	if done {
		return avail
	}
	_, err := q.o.API.GetObjectLimits(ctx, &objectsv1.GetObjectLimitsRequest{})
	if err != nil && !noObjectService(err) {
		return avail
	}
	q.setAvailable(err == nil)
	return err == nil
}

func (q *ObjectQueue) setAvailable(v bool) {
	q.mu.Lock()
	changed := v != q.available
	q.available, q.probed = v, true
	q.mu.Unlock()
	if !changed {
		return
	}
	b, _ := json.Marshal(map[string]bool{"available": v})
	if err := os.MkdirAll(q.o.Dir, 0o700); err == nil {
		_ = writeAtomic(filepath.Join(q.o.Dir, "server.json"), b)
	}
}

// noObjectService reports an error that says the server has no ObjectsService: an unknown path is a plain 404, which
// Connect reports as unimplemented. The not_found of a call of the service names an upload or an object.
func noObjectService(err error) bool {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusNotImplemented
	}
	return connect.CodeOf(err) == connect.CodeUnimplemented
}

func isInvalid(err error) bool {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return ae.Type == "invalid_request"
	}
	return connect.CodeOf(err) == connect.CodeInvalidArgument
}

type objectItem struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Path        string `json:"path"`
	// Spool: Path is under spool/, and goes when no item needs it. Otherwise it is the user's file, read when its turn
	// comes: SHA256 is then empty.
	Spool  bool   `json:"spool,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// Replaces is an earlier version of this scratch file, whose record goes once this one is up.
	Replaces string `json:"replaces,omitempty"`
}

func (q *ObjectQueue) itemPath(id string) string {
	return filepath.Join(q.o.Dir, "queue", id+".json")
}

func objectItemID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func (q *ObjectQueue) enqueue(it *objectItem) error {
	if err := os.MkdirAll(filepath.Join(q.o.Dir, "queue"), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(it)
	if err != nil {
		return err
	}
	if err := writeAtomic(q.itemPath(it.ID), b); err != nil {
		return err
	}
	q.Notify()
	return nil
}

// Notify makes Run look at the queue now.
func (q *ObjectQueue) Notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// AddBytes queues bytes held in memory and returns the reference to them: the reference is good once the queue has
// caught up, and a reader that comes earlier sees the object as not there yet.
func (q *ObjectQueue) AddBytes(sessionID, kind, name, mediaType string, data []byte) (*commonv1.ObjectRef, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	spool := filepath.Join(q.o.Dir, "spool")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(spool, sha)
	if _, err := os.Stat(path); err != nil {
		if err := writeAtomic(path, data); err != nil {
			return nil, err
		}
	}
	it := &objectItem{ID: objectItemID(sessionID, kind, name, sha), SessionID: sessionID, Kind: kind, Name: name, ContentType: mediaType, Path: path, Spool: true, SHA256: sha}
	if err := q.enqueue(it); err != nil {
		return nil, err
	}
	return &commonv1.ObjectRef{Sha256: sha, Size: int64(len(data)), MediaType: mediaType, Name: name}, nil
}

// AddSentFile queues a file of a SendUserFile call as it was sent, unless its path is sensitive (decision.IsSecretPath).
// False: it is not going to the store, and the caller treats it as staying on this machine.
func (q *ObjectQueue) AddSentFile(sessionID, path, mediaType string, data []byte) bool {
	if q == nil || decision.IsSecretPath(path) {
		return false
	}
	_, err := q.AddBytes(sessionID, ObjectSentFile, filepath.Base(path), mediaType, data)
	if err != nil {
		q.log.Warn("objects: cannot queue a sent file", "file", filepath.Base(path), "err", err)
		return false
	}
	return true
}

// --- scratch ----------------------------------------------------------------------------------------------

// ClientScratchFile names what the client itself keeps in a session's scratch directory, which is no file of the
// session's: output files of background tasks and the shell's state. rel is relative to the directory.
func ClientScratchFile(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return first == "tasks" || strings.HasPrefix(first, "shell-snapshot.") || strings.HasPrefix(first, "cwd-")
}

type scratchState struct {
	SessionID string `json:"session_id"`
	Dir       string `json:"dir"`
	// Closed: the session ended, so the directory is not looked at until it is opened again.
	Closed bool `json:"closed,omitempty"`
	// Files is what went into the queue, by path relative to Dir.
	Files map[string]scratchFile `json:"files,omitempty"`
}

type scratchFile struct {
	Size    int64 `json:"size"`
	ModTime int64 `json:"mod_time"` // unix nanoseconds
	// SHA256 is the version uploaded last.
	SHA256 string `json:"sha256,omitempty"`
}

func (q *ObjectQueue) scanPath(sessionID string) string {
	return filepath.Join(q.o.Dir, "scan", fileName(sessionID))
}

func (q *ObjectQueue) loadScan(sessionID string) (*scratchState, error) {
	b, err := os.ReadFile(q.scanPath(sessionID))
	if err != nil {
		return nil, err
	}
	st := &scratchState{}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	return st, nil
}

func (q *ObjectQueue) saveScan(st *scratchState) error {
	if err := os.MkdirAll(filepath.Join(q.o.Dir, "scan"), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeAtomic(q.scanPath(st.SessionID), b)
}

// TrackScratch makes the queue watch a session's scratch directory. The directory may not exist yet.
func (q *ObjectQueue) TrackScratch(sessionID, dir string) error {
	if q == nil || dir == "" {
		return nil
	}
	q.scanMu.Lock()
	defer q.scanMu.Unlock()
	if st, err := q.loadScan(sessionID); err == nil && st.Dir == dir {
		if !st.Closed {
			return nil
		}
		st.Closed = false
		return q.saveScan(st)
	}
	return q.saveScan(&scratchState{SessionID: sessionID, Dir: dir})
}

// CloseScratch stops watching a session's scratch directory after queueing what it has now, also what has just been
// written. Old sessions' directories would otherwise be walked forever.
func (q *ObjectQueue) CloseScratch(sessionID string) {
	if q == nil {
		return
	}
	q.scanMu.Lock()
	defer q.scanMu.Unlock()
	st, err := q.loadScan(sessionID)
	if err != nil {
		return
	}
	if q.Available() {
		q.scanOne(st, time.Now().Add(scratchSettle))
	}
	st.Closed = true
	_ = q.saveScan(st)
}

// scratchSkip says why a file of a scratch directory is not uploaded ("" when it is). Only regular files go: a link may
// lead anywhere, and a link to a secret must not bring it along.
func (q *ObjectQueue) scratchSkip(rel, path string, info fs.FileInfo) string {
	switch {
	case !info.Mode().IsRegular():
		return "not a regular file"
	case ClientScratchFile(rel):
		return "client internal"
	case decision.IsSecretPath(path) || decision.IsSecretPath(rel):
		return "sensitive path"
	case info.Size() > q.o.ScratchMaxBytes:
		return fmt.Sprintf("larger than the %d MB cap", q.o.ScratchMaxBytes>>20)
	}
	return ""
}

func (q *ObjectQueue) warnOnce(key, msg string, args ...any) {
	q.mu.Lock()
	seen := q.warned[key]
	q.warned[key] = true
	q.mu.Unlock()
	if !seen {
		q.log.Info(msg, args...)
	}
}

// scan queues the new and changed files of every watched scratch directory.
func (q *ObjectQueue) scan(now time.Time) {
	q.scanMu.Lock()
	defer q.scanMu.Unlock()
	entries, err := os.ReadDir(filepath.Join(q.o.Dir, "scan"))
	if err != nil {
		return
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(q.o.Dir, "scan", e.Name()))
		if err != nil {
			continue
		}
		st := &scratchState{}
		if json.Unmarshal(b, st) != nil || st.SessionID == "" || st.Closed {
			continue
		}
		q.scanOne(st, now)
	}
}

func (q *ObjectQueue) scanOne(st *scratchState, now time.Time) {
	changed := false
	_ = filepath.WalkDir(st.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // gone meanwhile, or unreadable: the next scan looks again
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(st.Dir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if why := q.scratchSkip(rel, path, info); why != "" {
			if !ClientScratchFile(rel) {
				q.warnOnce(st.SessionID+"/"+rel, "objects: scratch file stays local", "session", st.SessionID, "file", rel, "why", why)
			}
			return nil
		}
		if now.Sub(info.ModTime()) < scratchSettle {
			return nil
		}
		seen, known := st.Files[rel]
		if known && seen.Size == info.Size() && seen.ModTime == info.ModTime().UnixNano() {
			return nil
		}
		kind := ObjectScratch
		if strings.HasPrefix(rel, "tool-results/") {
			kind = ObjectToolResult
		}
		it := &objectItem{ID: objectItemID(st.SessionID, kind, rel), SessionID: st.SessionID, Kind: kind, Name: rel, Path: path, Replaces: seen.SHA256}
		if err := q.enqueue(it); err != nil {
			return nil
		}
		if st.Files == nil {
			st.Files = map[string]scratchFile{}
		}
		st.Files[rel] = scratchFile{Size: info.Size(), ModTime: info.ModTime().UnixNano(), SHA256: seen.SHA256}
		changed = true
		return nil
	})
	if changed {
		_ = q.saveScan(st)
	}
}

// uploaded remembers the version of a scratch file that went up, so the next one can replace it.
func (q *ObjectQueue) uploaded(it *objectItem, sha string) {
	if it.Spool {
		return
	}
	q.scanMu.Lock()
	defer q.scanMu.Unlock()
	st, err := q.loadScan(it.SessionID)
	if err != nil {
		return
	}
	f := st.Files[it.Name]
	f.SHA256 = sha
	if st.Files == nil {
		st.Files = map[string]scratchFile{}
	}
	st.Files[it.Name] = f
	_ = q.saveScan(st)
}

// Forget drops what is queued and watched for a deleted session; the server deletes what already went up.
func (q *ObjectQueue) Forget(sessionID string) {
	if q != nil {
		forgetObjects(q.o.Dir, sessionID)
	}
}

func forgetObjects(dir, sessionID string) {
	_ = os.Remove(filepath.Join(dir, "scan", fileName(sessionID)))
	entries, err := os.ReadDir(filepath.Join(dir, "queue"))
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, "queue", e.Name())
		if it := readItem(path); it != nil && it.SessionID == sessionID {
			_ = os.Remove(path)
			dropSpool(dir, it)
		}
	}
}

func readItem(path string) *objectItem {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	it := &objectItem{}
	if json.Unmarshal(b, it) != nil || it.ID == "" {
		return nil
	}
	return it
}

// dropSpool removes a spooled file no pending item uses anymore.
func dropSpool(dir string, it *objectItem) {
	if !it.Spool {
		return
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "queue"))
	for _, e := range entries {
		if other := readItem(filepath.Join(dir, "queue", e.Name())); other != nil && other.Path == it.Path {
			return
		}
	}
	_ = os.Remove(it.Path)
}

// --- upload -----------------------------------------------------------------------------------------------

// Run uploads until ctx is done: at start with what an earlier run left, on Notify, and every Interval.
func (q *ObjectQueue) Run(ctx context.Context) error {
	ticker := time.NewTicker(q.o.Interval)
	defer ticker.Stop()
	for {
		q.Flush(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-q.wake:
		case <-ticker.C:
		}
	}
}

// Pending returns how many uploads are queued.
func (q *ObjectQueue) Pending() int {
	entries, err := os.ReadDir(filepath.Join(q.o.Dir, "queue"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// Flush scans the watched directories and sends what is queued, oldest first, until the queue is empty or the server
// stops answering; what is left waits for the next call. Nothing happens against a server without a store.
func (q *ObjectQueue) Flush(ctx context.Context) {
	if q == nil {
		return
	}
	q.flushMu.Lock()
	defer q.flushMu.Unlock()
	if !q.Probe(ctx) {
		return
	}
	q.scan(time.Now())
	entries, err := os.ReadDir(filepath.Join(q.o.Dir, "queue"))
	if err != nil {
		return
	}
	type pending struct {
		path string
		at   time.Time
	}
	var todo []pending
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		todo = append(todo, pending{filepath.Join(q.o.Dir, "queue", e.Name()), info.ModTime()})
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].at.Before(todo[j].at) })
	for _, p := range todo {
		if ctx.Err() != nil {
			return
		}
		it := readItem(p.path)
		if it == nil {
			_ = os.Remove(p.path)
			continue
		}
		err := q.uploadItem(ctx, it)
		switch {
		case err == nil:
			_ = os.Remove(p.path)
			dropSpool(q.o.Dir, it)
		case errors.Is(err, os.ErrNotExist):
			// A scratch file deleted meanwhile, or a spool file removed by hand: nothing left to send.
			_ = os.Remove(p.path)
		case isInvalid(err):
			// Too large for the server, or refused for good: retrying changes nothing, and the file stays local.
			q.log.Warn("objects: the server refused a file; it stays local", "session", it.SessionID, "file", it.Name, "err", err)
			_ = os.Remove(p.path)
			dropSpool(q.o.Dir, it)
		case noObjectService(err):
			q.setAvailable(false)
			q.mu.Lock()
			q.probed = false
			q.mu.Unlock()
			return
		default:
			q.log.Debug("objects: upload failed; will retry", "session", it.SessionID, "file", it.Name, "err", err)
			return // offline, over quota, server trouble: the same goes for the rest of the queue
		}
	}
}

func objectUse(it *objectItem, contentType string) *objectsv1.ObjectUse {
	return &objectsv1.ObjectUse{SessionId: it.SessionID, Kind: objectKindProto[it.Kind], Name: it.Name, ContentType: contentType}
}

func (q *ObjectQueue) uploadItem(ctx context.Context, it *objectItem) error {
	f, err := os.Open(it.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: %w", it.Name, os.ErrNotExist)
	}
	sha, contentType := it.SHA256, it.ContentType
	if sha == "" {
		h := sha256.New()
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		h.Write(head[:n])
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		sha = hex.EncodeToString(h.Sum(nil))
		if contentType == "" {
			contentType = fileContentType(it.Name, head[:n])
		}
	}
	use := objectUse(it, contentType)
	// Twice at most: an upload another device finished in the meantime answers NOT_FOUND, and starting over then finds
	// the bytes there.
	for attempt := 0; ; attempt++ {
		err := q.send(ctx, f, sha, info.Size(), use)
		if err == nil {
			break
		}
		if attempt == 0 && IsNotFound(err) {
			continue
		}
		if !it.Spool && isInvalid(err) {
			// The file changed while it was read. A new scan finds the new version.
			if st, serr := os.Stat(it.Path); serr == nil && (st.Size() != info.Size() || !st.ModTime().Equal(info.ModTime())) {
				return fmt.Errorf("%s: %w", it.Name, os.ErrNotExist)
			}
		}
		return err
	}
	q.uploaded(it, sha)
	if it.Replaces != "" && it.Replaces != sha {
		if _, err := q.o.API.DeleteObject(ctx, &objectsv1.DeleteObjectRequest{Sha256: it.Replaces, SessionId: it.SessionID, Kind: objectKindProto[it.Kind], Name: it.Name}); err != nil && !IsNotFound(err) {
			q.log.Debug("objects: cannot remove the previous version", "file", it.Name, "err", err)
		}
	}
	return nil
}

func (q *ObjectQueue) send(ctx context.Context, f *os.File, sha string, size int64, use *objectsv1.ObjectUse) error {
	start, err := q.o.API.StartUpload(ctx, &objectsv1.StartUploadRequest{Sha256: sha, Size: size, Use: use})
	if err != nil {
		return err
	}
	if start.GetAlreadyStored() {
		return nil
	}
	chunk := start.GetChunkSize()
	if chunk <= 0 {
		return errors.New("the server gave no chunk size")
	}
	have := map[int32]bool{}
	for _, i := range start.GetReceivedChunks() {
		have[i] = true
	}
	buf := make([]byte, chunk)
	for i := int32(0); int64(i)*chunk < size; i++ {
		if have[i] {
			continue
		}
		n, err := f.ReadAt(buf, int64(i)*chunk)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if _, err := q.o.API.UploadChunk(ctx, &objectsv1.UploadChunkRequest{UploadId: start.GetUploadId(), Index: i, Data: buf[:n]}); err != nil {
			return err
		}
	}
	_, err = q.o.API.FinishUpload(ctx, &objectsv1.FinishUploadRequest{UploadId: start.GetUploadId(), Use: use})
	return err
}

// fileContentType reads a type from a scratch file's first bytes, and a text type from its name. The read route decides
// what renders; this only has to be right for what a reader of the object expects.
func fileContentType(name string, head []byte) string {
	t := http.DetectContentType(head)
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	if t == "text/plain" {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".md", ".markdown":
			return "text/markdown"
		case ".csv":
			return "text/csv"
		case ".json":
			return "application/json"
		}
	}
	return t
}

// --- transcripts ------------------------------------------------------------------------------------------

// objectStandIn is the text of a block whose bytes went to the store. The block stays a text block with an `object`
// field, so anything that does not know objects reads a sentence: a model resuming the session on another machine, an
// older client.
func objectStandIn(ref *commonv1.ObjectRef) string {
	return fmt.Sprintf("[%s stored as object %s]", ref.GetMediaType(), ref.GetSha256())
}

// StandInBlock is a transcript block in place of bytes: see objectStandIn.
func StandInBlock(ref *commonv1.ObjectRef) map[string]any {
	return map[string]any{"type": "text", "text": objectStandIn(ref), "object": map[string]any{
		"sha256": ref.GetSha256(), "size": ref.GetSize(), "media_type": ref.GetMediaType(), "name": ref.GetName(),
	}}
}

// referObjects replaces the images and documents of a transcript line by references to the store and queues their bytes:
// a user message's are attachments, a tool result's are images the model read or captured. Typed text stays. Other lines
// come back unchanged, and so does one this cannot parse.
func (q *ObjectQueue) referObjects(sessionID, line string) string {
	if !strings.Contains(line, `"base64"`) {
		return line
	}
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var rec map[string]any
	if dec.Decode(&rec) != nil {
		return line
	}
	msg, _ := rec["message"].(map[string]any)
	blocks, _ := msg["content"].([]any)
	changed := false
	for i, raw := range blocks {
		block, _ := raw.(map[string]any)
		if b := q.refer(sessionID, ObjectAttachment, block); b != nil {
			blocks[i] = b
			changed = true
			continue
		}
		parts, _ := block["content"].([]any)
		for j, p := range parts {
			part, _ := p.(map[string]any)
			if b := q.refer(sessionID, ObjectImage, part); b != nil {
				parts[j] = b
				changed = true
			}
		}
	}
	if !changed {
		return line
	}
	out, err := json.Marshal(rec)
	if err != nil {
		return line
	}
	return string(out)
}

func (q *ObjectQueue) refer(sessionID, kind string, block map[string]any) map[string]any {
	if t := block["type"]; t != "image" && t != "document" {
		return nil
	}
	src, _ := block["source"].(map[string]any)
	if src["type"] != "base64" {
		return nil
	}
	encoded, _ := src["data"].(string)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	mediaType, _ := src["media_type"].(string)
	name, _ := block["title"].(string)
	ref, err := q.AddBytes(sessionID, kind, name, mediaType, data)
	if err != nil {
		q.log.Warn("objects: cannot queue an image; it stays out of the server's transcript", "session", sessionID, "err", err)
		return map[string]any{"type": "text", "text": screenshotKept}
	}
	return StandInBlock(ref)
}
