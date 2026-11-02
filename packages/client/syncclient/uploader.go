package syncclient

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/workspace"
)

// SessionAPI is the part of SyncService the uploader needs (implemented by
// *Client).
type SessionAPI interface {
	UpsertSession(ctx context.Context, req *syncv1.UpsertSessionRequest) (*syncv1.UpsertSessionResponse, error)
	AppendSessionEvents(ctx context.Context, req *syncv1.AppendSessionEventsRequest) (*syncv1.AppendSessionEventsResponse, error)
}

// DiffStats mirrors harness.agent.v1.DiffStats.
type DiffStats struct {
	FilesChanged int32 `json:"files_changed"`
	Additions    int32 `json:"additions"`
	Deletions    int32 `json:"deletions"`
}

// SessionMeta is the client-owned session metadata.
type SessionMeta struct {
	Title               string                 `json:"title,omitempty"`
	Cwd                 string                 `json:"cwd,omitempty"`
	Repo                string                 `json:"repo,omitempty"`
	Branch              string                 `json:"branch,omitempty"`
	Model               string                 `json:"model,omitempty"`
	PermissionMode      agentv1.PermissionMode `json:"permission_mode,omitempty"`
	Status              agentv1.SessionStatus  `json:"status,omitempty"`
	DeviceID            string                 `json:"device_id,omitempty"`
	DeviceName          string                 `json:"device_name,omitempty"`
	ParentSessionID     string                 `json:"parent_session_id,omitempty"`
	PromptBundleVersion string                 `json:"prompt_bundle_version,omitempty"`
	DiffStats           *DiffStats             `json:"diff_stats,omitempty"`
	// UpdatedAt is RFC 3339 UTC; filled automatically when empty.
	UpdatedAt string `json:"updated_at,omitempty"`
}

// fieldPaths maps update paths to accessors used for change detection.
var fieldPaths = []struct {
	path string
	get  func(m *SessionMeta) any
}{
	{"info.title", func(m *SessionMeta) any { return m.Title }},
	{"info.cwd", func(m *SessionMeta) any { return m.Cwd }},
	{"info.repo", func(m *SessionMeta) any { return m.Repo }},
	{"info.git_branch", func(m *SessionMeta) any { return m.Branch }},
	{"info.model", func(m *SessionMeta) any { return m.Model }},
	{"info.permission_mode", func(m *SessionMeta) any { return m.PermissionMode }},
	{"info.status", func(m *SessionMeta) any { return m.Status }},
	{"info.device_id", func(m *SessionMeta) any { return m.DeviceID }},
	{"info.device_name", func(m *SessionMeta) any { return m.DeviceName }},
	{"info.parent_session_id", func(m *SessionMeta) any { return m.ParentSessionID }},
	{"prompt_bundle_version", func(m *SessionMeta) any { return m.PromptBundleVersion }},
	{"info.diff_stats", func(m *SessionMeta) any {
		if m.DiffStats == nil {
			return DiffStats{}
		}
		return *m.DiffStats
	}},
	{"info.updated_at", func(m *SessionMeta) any { return m.UpdatedAt }},
}

func isZero(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case agentv1.PermissionMode:
		return x == 0
	case agentv1.SessionStatus:
		return x == 0
	case DiffStats:
		return x == DiffStats{}
	}
	return false
}

func (m *SessionMeta) toProto(id string) *syncv1.SessionMetadata {
	info := &agentv1.SessionInfo{
		Id: id, Title: m.Title, Cwd: m.Cwd, GitBranch: m.Branch, Model: m.Model,
		PermissionMode: m.PermissionMode, Status: m.Status, UpdatedAt: m.UpdatedAt, Repo: m.Repo,
		DeviceId: m.DeviceID, DeviceName: m.DeviceName, ParentSessionId: m.ParentSessionID,
	}
	if m.DiffStats != nil {
		info.DiffStats = &agentv1.DiffStats{FilesChanged: m.DiffStats.FilesChanged, Additions: m.DiffStats.Additions, Deletions: m.DiffStats.Deletions}
	}
	return &syncv1.SessionMetadata{Info: info, PromptBundleVersion: m.PromptBundleVersion}
}

// uploadState is persisted per session in <StateDir>/<id>.json.
type uploadState struct {
	SessionID string      `json:"session_id"`
	Path      string      `json:"path"`
	Offset    int64       `json:"offset"`   // bytes of the transcript acknowledged by the server
	LastSeq   int64       `json:"last_seq"` // server seq after the last acknowledged batch
	Meta      SessionMeta `json:"meta"`
	Upserted  bool        `json:"upserted"`
	Dirty     []string    `json:"dirty,omitempty"` // update paths not yet sent
	// Derive fills empty metadata from the transcript (imported sessions).
	Derive bool `json:"derive,omitempty"`
}

// UploaderOptions configure an Uploader.
type UploaderOptions struct {
	API SessionAPI
	// StateDir holds per-session upload state; default DefaultDir()/sessions.
	StateDir string
	// MaxEntries / MaxBytes bound one AppendSessionEvents call
	// (defaults 500 entries / 4 MiB minus headroom).
	MaxEntries int
	MaxBytes   int
	// Interval is the retry / poll period of Run (default 30 s).
	Interval time.Duration
	// DeviceID / DeviceName fill SessionMeta when empty.
	DeviceID, DeviceName string
	// ResolveRepo maps a cwd to the normalized repo; default workspace.Repo.
	ResolveRepo func(ctx context.Context, cwd string) string
	Logger      *slog.Logger
}

// Uploader uploads local JSONL transcripts to the server. Uploads are
// idempotent by entry uuid and resume from the last acknowledged byte
// offset stored locally, so crashes and retries never lose or duplicate
// entries. Methods are safe for concurrent use.
type Uploader struct {
	o   UploaderOptions
	log *slog.Logger

	mu      sync.Mutex
	pending map[string]bool
	wake    chan struct{}

	flushMu sync.Mutex // serializes uploads
}

// NewUploader creates an uploader.
func NewUploader(o UploaderOptions) *Uploader {
	if o.StateDir == "" {
		o.StateDir = filepath.Join(DefaultDir(), "sessions")
	}
	if o.MaxEntries <= 0 || o.MaxEntries > 500 {
		o.MaxEntries = 500
	}
	if o.MaxBytes <= 0 || o.MaxBytes > 4<<20 {
		o.MaxBytes = 4<<20 - 64<<10
	}
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.ResolveRepo == nil {
		o.ResolveRepo = workspace.Repo
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Uploader{o: o, log: log, pending: map[string]bool{}, wake: make(chan struct{}, 1)}
}

func statePath(dir, id string) string { return filepath.Join(dir, fileName(id)) }

func loadState(dir, id string) (*uploadState, error) {
	b, err := os.ReadFile(statePath(dir, id))
	if err != nil {
		return nil, err
	}
	st := &uploadState{}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	return st, nil
}

func saveState(dir string, st *uploadState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(statePath(dir, st.SessionID), b)
}

// QueueFile registers a transcript for upload without metadata; the
// uploader derives cwd, branch and title from the transcript. Existing
// state for the session is kept. Used by `harness import claude --sessions`.
func QueueFile(stateDir, sessionID, path string) error {
	if stateDir == "" {
		stateDir = filepath.Join(DefaultDir(), "sessions")
	}
	if _, err := loadState(stateDir, sessionID); err == nil {
		return nil
	}
	return saveState(stateDir, &uploadState{SessionID: sessionID, Path: path, Derive: true})
}

// Queue registers a transcript for upload like QueueFile, using the
// uploader's state directory (importer.SessionQueue).
func (u *Uploader) Queue(sessionID, path string) error {
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	return QueueFile(u.o.StateDir, sessionID, path)
}

// Track registers a live session for upload and schedules a metadata
// upsert. Call it when a session opens (also on resume).
func (u *Uploader) Track(sessionID, path string, meta SessionMeta) error {
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	st, err := loadState(u.o.StateDir, sessionID)
	if err != nil {
		st = &uploadState{SessionID: sessionID}
	}
	st.Path = path
	st.Derive = false
	u.fillMeta(&meta)
	u.mergeMeta(st, meta)
	if err := saveState(u.o.StateDir, st); err != nil {
		return err
	}
	u.Notify(sessionID)
	return nil
}

func (u *Uploader) fillMeta(m *SessionMeta) {
	if m.DeviceID == "" {
		m.DeviceID = u.o.DeviceID
	}
	if m.DeviceName == "" {
		m.DeviceName = u.o.DeviceName
	}
}

// mergeMeta replaces st.Meta with meta and marks changed fields dirty.
func (u *Uploader) mergeMeta(st *uploadState, meta SessionMeta) {
	meta.UpdatedAt = st.Meta.UpdatedAt
	for _, f := range fieldPaths {
		if f.path == "info.updated_at" {
			continue
		}
		nv, ov := f.get(&meta), f.get(&st.Meta)
		if nv != ov && !(isZero(nv) && !st.Upserted) {
			if !slices.Contains(st.Dirty, f.path) {
				st.Dirty = append(st.Dirty, f.path)
			}
		}
	}
	if len(st.Dirty) > 0 {
		meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if !slices.Contains(st.Dirty, "info.updated_at") {
			st.Dirty = append(st.Dirty, "info.updated_at")
		}
	}
	st.Meta = meta
}

// UpdateMeta changes the metadata of a tracked session (title, status,
// branch, diff stats, ...) and schedules an upsert of the changed fields.
func (u *Uploader) UpdateMeta(sessionID string, fn func(*SessionMeta)) error {
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	st, err := loadState(u.o.StateDir, sessionID)
	if err != nil {
		return notTracked(sessionID, err)
	}
	m := st.Meta
	if m.DiffStats != nil {
		ds := *m.DiffStats
		m.DiffStats = &ds
	}
	fn(&m)
	u.mergeMeta(st, m)
	if err := saveState(u.o.StateDir, st); err != nil {
		return err
	}
	u.Notify(sessionID)
	return nil
}

// Notify schedules an upload of sessionID (new transcript lines or
// metadata). It never blocks.
func (u *Uploader) Notify(sessionID string) {
	u.mu.Lock()
	u.pending[sessionID] = true
	u.mu.Unlock()
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Pending returns the session ids with unacknowledged data on disk.
func (u *Uploader) Pending() []string {
	entries, _ := os.ReadDir(u.o.StateDir)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(u.o.StateDir, e.Name()))
		if err != nil {
			continue
		}
		var st uploadState
		if json.Unmarshal(b, &st) != nil || st.SessionID == "" {
			continue
		}
		if !st.Upserted || len(st.Dirty) > 0 {
			out = append(out, st.SessionID)
			continue
		}
		if hasCompleteLine(st.Path, st.Offset) {
			out = append(out, st.SessionID)
		}
	}
	sort.Strings(out)
	return out
}

// hasCompleteLine reports whether path has a newline-terminated line after offset.
func hasCompleteLine(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() <= offset {
		return false
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 && slices.Contains(buf[:n], '\n') {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// Run uploads notified sessions until ctx is done. It starts with every
// session that has pending data on disk and retries failures every
// Interval (offline-tolerant).
func (u *Uploader) Run(ctx context.Context) error {
	for _, id := range u.Pending() {
		u.mu.Lock()
		u.pending[id] = true
		u.mu.Unlock()
	}
	ticker := time.NewTicker(u.o.Interval)
	defer ticker.Stop()
	for {
		u.mu.Lock()
		batch := make([]string, 0, len(u.pending))
		for id := range u.pending {
			batch = append(batch, id)
		}
		u.pending = map[string]bool{}
		u.mu.Unlock()
		sort.Strings(batch)
		for _, id := range batch {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := u.Flush(ctx, id); err != nil {
				u.log.Debug("sync: session upload failed; will retry", "session", id, "err", err)
				u.mu.Lock()
				u.pending[id] = true
				u.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-u.wake:
		case <-ticker.C:
			for _, id := range u.Pending() {
				u.mu.Lock()
				u.pending[id] = true
				u.mu.Unlock()
			}
		}
	}
}

// Flush synchronously uploads everything pending for sessionID: the
// metadata upsert (if needed) and all complete transcript lines.
func (u *Uploader) Flush(ctx context.Context, sessionID string) error {
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	st, err := loadState(u.o.StateDir, sessionID)
	if err != nil {
		return notTracked(sessionID, err)
	}
	if st.Derive {
		u.derive(ctx, st)
	}
	if !st.Upserted || len(st.Dirty) > 0 {
		if err := u.upsert(ctx, st); err != nil {
			return err
		}
	}
	for {
		entries, consumed, err := u.readBatch(st)
		if err != nil {
			return err
		}
		if consumed == 0 {
			return nil
		}
		if len(entries) > 0 {
			resp, err := u.o.API.AppendSessionEvents(ctx, &syncv1.AppendSessionEventsRequest{SessionId: sessionID, Entries: entries})
			if err != nil && IsNotFound(err) {
				// The server lost or never had the session: upsert everything.
				st.Upserted = false
				if err := u.upsert(ctx, st); err != nil {
					return err
				}
				resp, err = u.o.API.AppendSessionEvents(ctx, &syncv1.AppendSessionEventsRequest{SessionId: sessionID, Entries: entries})
			}
			if err != nil {
				return err
			}
			st.LastSeq = resp.GetLastSeq()
		}
		st.Offset += consumed
		if err := saveState(u.o.StateDir, st); err != nil {
			return err
		}
	}
}

func (u *Uploader) upsert(ctx context.Context, st *uploadState) error {
	m := st.Meta
	u.fillMeta(&m)
	if m.UpdatedAt == "" {
		m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	var paths []string
	if st.Upserted {
		paths = slices.Clone(st.Dirty)
	} else {
		// First upsert: only fields we know, so that values set elsewhere
		// (a title renamed on the web) are not clobbered by blanks.
		for _, f := range fieldPaths {
			if !isZero(f.get(&m)) {
				paths = append(paths, f.path)
			}
		}
	}
	if len(paths) == 0 {
		paths = []string{"info.updated_at"}
	}
	sort.Strings(paths)
	_, err := u.o.API.UpsertSession(ctx, &syncv1.UpsertSessionRequest{Session: m.toProto(st.SessionID), UpdatePaths: paths})
	if err != nil {
		return err
	}
	st.Meta = m
	st.Upserted = true
	st.Dirty = nil
	return saveState(u.o.StateDir, st)
}

// readBatch reads complete lines after st.Offset up to the batch limits.
// consumed counts bytes of all lines read (including skipped ones).
func (u *Uploader) readBatch(st *uploadState) ([]*syncv1.TranscriptEntry, int64, error) {
	f, err := os.Open(st.Path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(st.Offset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	var entries []*syncv1.TranscriptEntry
	var consumed int64
	size := 0
	for len(entries) < u.o.MaxEntries {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// EOF or a partial last line: stop before it.
			break
		}
		text := strings.TrimRight(string(line), "\r\n")
		if len(text)+256 > u.o.MaxBytes {
			u.log.Warn("sync: transcript line too large to upload; skipped", "session", st.SessionID, "bytes", len(text))
			consumed += int64(len(line))
			continue
		}
		if size+len(text)+256 > u.o.MaxBytes && len(entries) > 0 {
			break
		}
		offset := st.Offset + consumed
		consumed += int64(len(line))
		if strings.TrimSpace(text) == "" {
			continue
		}
		e, ok := entryFromLine(st.SessionID, offset, text)
		if !ok {
			u.log.Warn("sync: malformed transcript line skipped", "session", st.SessionID, "offset", offset)
			continue
		}
		size += len(text) + 256
		entries = append(entries, e)
	}
	return entries, consumed, nil
}

var bookkeepingNS = uuid.MustParse("6f1d4c2e-7c1a-4f63-9b8e-2d5f0a9e3b71")

// entryFromLine converts one JSONL line. Lines without a uuid (bookkeeping
// records) get a deterministic one derived from the session, the byte
// offset and the line content, so retries stay idempotent.
func entryFromLine(sessionID string, offset int64, line string) (*syncv1.TranscriptEntry, bool) {
	var rec struct {
		UUID       string  `json:"uuid"`
		ParentUUID *string `json:"parentUuid"`
		Type       string  `json:"type"`
		Timestamp  string  `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return nil, false
	}
	id := rec.UUID
	if id == "" {
		sum := sha256.Sum256([]byte(line))
		id = uuid.NewSHA1(bookkeepingNS, []byte(fmt.Sprintf("%s:%d:%x", sessionID, offset, sum[:8]))).String()
	}
	e := &syncv1.TranscriptEntry{Uuid: id, Type: rec.Type, EntryJson: line}
	if rec.ParentUUID != nil {
		e.ParentUuid = *rec.ParentUUID
	}
	if rec.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			e.Timestamp = t.UTC().Format(time.RFC3339Nano)
		} else {
			e.Timestamp = rec.Timestamp
		}
	}
	return e, true
}

// derive fills empty metadata of an imported session from its transcript.
func (u *Uploader) derive(ctx context.Context, st *uploadState) {
	d := DeriveMeta(st.Path)
	m := &st.Meta
	if m.Cwd == "" {
		m.Cwd = d.Cwd
	}
	if m.Branch == "" {
		m.Branch = d.Branch
	}
	if m.Title == "" {
		m.Title = d.Title
	}
	if m.Model == "" {
		m.Model = d.Model
	}
	if m.UpdatedAt == "" {
		m.UpdatedAt = d.UpdatedAt
	}
	if m.Status == 0 {
		m.Status = agentv1.SessionStatus_SESSION_STATUS_IDLE
	}
	if m.Repo == "" && m.Cwd != "" {
		if fi, err := os.Stat(m.Cwd); err == nil && fi.IsDir() {
			m.Repo = u.o.ResolveRepo(ctx, m.Cwd)
		}
	}
	st.Derive = false
}

// DeriveMeta extracts metadata from a Claude Code / harness JSONL
// transcript: the last cwd and branch, the model of the last assistant
// message, the newest timestamp and a title (custom title, AI title,
// summary, or the first user prompt).
func DeriveMeta(path string) SessionMeta {
	var m SessionMeta
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var customTitle, aiTitle, summary, firstPrompt string
	var newest time.Time
	for sc.Scan() {
		var rec struct {
			Type        string `json:"type"`
			Cwd         string `json:"cwd"`
			GitBranch   string `json:"gitBranch"`
			Timestamp   string `json:"timestamp"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
			Title       string `json:"title"`
			Summary     string `json:"summary"`
			IsMeta      bool   `json:"isMeta"`
			Message     *struct {
				Role    string          `json:"role"`
				Model   string          `json:"model"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec.Cwd != "" {
			m.Cwd = rec.Cwd
		}
		if rec.GitBranch != "" {
			m.Branch = rec.GitBranch
		}
		if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil && t.After(newest) {
			newest = t
		}
		switch rec.Type {
		case "custom-title":
			customTitle = firstNonEmpty(rec.CustomTitle, rec.Title)
		case "ai-title":
			aiTitle = firstNonEmpty(rec.AITitle, rec.Title)
		case "summary":
			summary = rec.Summary
		case "assistant":
			if rec.Message != nil && rec.Message.Model != "" && rec.Message.Model != "<synthetic>" {
				m.Model = rec.Message.Model
			}
		case "user":
			if firstPrompt == "" && !rec.IsMeta && rec.Message != nil {
				var s string
				if json.Unmarshal(rec.Message.Content, &s) == nil && !strings.HasPrefix(strings.TrimSpace(s), "<") {
					firstPrompt = s
				}
			}
		}
	}
	m.Title = firstNonEmpty(customTitle, aiTitle, summary, truncateTitle(firstPrompt))
	if !newest.IsZero() {
		m.UpdatedAt = newest.UTC().Format(time.RFC3339)
	}
	return m
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func truncateTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return s
}

// ErrNotTracked is returned for sessions without upload state.
var ErrNotTracked = errors.New("sync: session is not tracked")

func notTracked(id string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotTracked, id)
	}
	return fmt.Errorf("sync: upload state of %s: %w", id, err)
}
