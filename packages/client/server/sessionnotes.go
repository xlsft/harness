package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// How a note reaches a session that is not open here, in the order routeNote tries:
//
//   - The relay (SendSessionNote), for a session of another machine that is registered there: it waits in core while
//     the machine is offline and arrives as the same reminder, through Host.exec and DeliverSessionNote.
//   - The user's SESSION_NOTES document for the session, on the harness server, for a session that is not open anywhere:
//     whichever device opens the session delivers the notes and deletes the document, and a fork of it on another
//     machine (the way a session continues elsewhere) gets a copy.
//   - HARNESS_HOME/session-links/<id>.pending.json for a session on this machine, without a server or while it cannot be
//     reached.
//   - The sender's outbox, retried when it opens or ends a turn, when nothing above works now.
//
// A note has an id and a session delivers each id once, so a retry or a second route cannot repeat it.

// sessionNoteDocs is the part of SyncService the document route needs (implemented by *syncclient.Client).
type sessionNoteDocs interface {
	GetDocument(ctx context.Context, req *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error)
	PutDocument(ctx context.Context, req *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error)
	DeleteDocument(ctx context.Context, req *syncv1.DeleteDocumentRequest) (*syncv1.DeleteDocumentResponse, error)
}

const (
	noteDocAttempts = 3
	// maxDocNotes bounds a session's document; the oldest notes are dropped.
	maxDocNotes = 100
	// maxSeenNotes is how many delivered note ids a session remembers.
	maxSeenNotes = 200
	// maxOutbox bounds what a session keeps to retry.
	maxOutbox = 50
)

type noteDoc struct {
	Notes []session.SessionNote `json:"notes"`
}

type outboxNote struct {
	To   string              `json:"to"`
	Note session.SessionNote `json:"note"`
}

func (s *Server) noteDocs() sessionNoteDocs {
	d, _ := s.Sync.(sessionNoteDocs)
	return d
}

func noteDocRef(id string) *syncv1.DocumentRef {
	return &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SESSION_NOTES, Key: id}
}

// readNoteDoc returns the session's document; nothing and no error when it has none.
func readNoteDoc(ctx context.Context, docs sessionNoteDocs, id string) (noteDoc, *syncv1.Document, error) {
	res, err := docs.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: noteDocRef(id)}})
	if err != nil {
		if syncclient.IsNotFound(err) {
			return noteDoc{}, nil, nil
		}
		return noteDoc{}, nil, err
	}
	d := res.GetDocument()
	if d == nil {
		return noteDoc{}, nil, nil
	}
	var doc noteDoc
	if err := json.Unmarshal([]byte(d.GetContent()), &doc); err != nil {
		return noteDoc{}, d, fmt.Errorf("the notes of session %s are not readable: %w", id, err)
	}
	return doc, d, nil
}

// putNoteDoc adds a note to the session's document, with a version check against other devices writing it.
func (s *Server) putNoteDoc(ctx context.Context, id string, n session.SessionNote) error {
	docs := s.noteDocs()
	if docs == nil {
		return errors.New("this client does not sync with a harness server")
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	for range noteDocAttempts {
		doc, cur, err := readNoteDoc(ctx, docs, id)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(doc.Notes, func(x session.SessionNote) bool { return x.ID == n.ID }) {
			return nil
		}
		doc.Notes = append(doc.Notes, n)
		if len(doc.Notes) > maxDocNotes {
			doc.Notes = doc.Notes[len(doc.Notes)-maxDocNotes:]
		}
		data, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		res, err := docs.PutDocument(ctx, &syncv1.PutDocumentRequest{Ref: noteDocRef(id), Content: string(data), BaseVersion: proto.Int64(cur.GetVersion())})
		if err != nil {
			return err
		}
		if res.GetDocument() != nil {
			return nil
		}
	}
	return errors.New("the session's notes kept changing on the server")
}

// pendingMu orders the pending files of closed sessions.
var pendingMu sync.Mutex

func pendingNotesPath(id string) string {
	return strings.TrimSuffix(sessionLinksPath(id), ".json") + ".pending.json"
}

func appendPendingNote(id string, n session.SessionNote) error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	var notes []session.SessionNote
	if data, err := os.ReadFile(pendingNotesPath(id)); err == nil {
		_ = json.Unmarshal(data, &notes)
	}
	data, err := json.Marshal(append(notes, n))
	if err != nil {
		return err
	}
	return writeLinksFile(pendingNotesPath(id), data)
}

func takePendingNotes(id string) []session.SessionNote {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	data, err := os.ReadFile(pendingNotesPath(id))
	if err != nil {
		return nil
	}
	var notes []session.SessionNote
	_ = json.Unmarshal(data, &notes)
	_ = os.Remove(pendingNotesPath(id))
	return notes
}

// sendRelayNote hands the note to the relay; core keeps it until the session's machine acks it.
func (s *Server) sendRelayNote(ctx context.Context, id string, n session.SessionNote) error {
	if s.Relay == nil {
		return errors.New("remote control is off on this client")
	}
	ctx, cancel := context.WithTimeout(ctx, relayCommandTimeout)
	defer cancel()
	res, err := relayCall(ctx, s.Relay, relayv1connect.RelayServiceClient.SendSessionNote, &relayv1.SendSessionNoteRequest{
		Request: &agentv1.DeliverSessionNoteRequest{SessionId: id, Note: noteToProto(n)}, Options: commandOptions(n.ID)})
	if err != nil {
		return err
	}
	return commandOutcome(res.GetResult())
}

// routeNote takes care of a note for a session that is not open here. An error means no route works now.
func (s *Server) routeNote(ctx context.Context, id string, n session.SessionNote) error {
	_, path := s.locate(id)
	if path == "" {
		s.refreshListing(ctx, "")
		if s.remoteInfo(id) != nil {
			err := s.sendRelayNote(ctx, id, n)
			if err == nil {
				return nil
			}
			s.log().Debug("session note: not through the relay", "session", id, "err", err)
		}
	}
	err := s.putNoteDoc(ctx, id, n)
	if err == nil {
		return nil
	}
	s.log().Debug("session note: not in the server's document", "session", id, "err", err)
	if path != "" {
		return appendPendingNote(id, n)
	}
	return err
}

// routeOrKeep routes the note, or keeps it in the sender's outbox to try again.
func (ls *liveSession) routeOrKeep(ctx context.Context, id string, n session.SessionNote) {
	if err := ls.srv.routeNote(ctx, id, n); err != nil {
		ls.srv.log().Warn("session note: no route for now; it is kept to retry", "session", id, "err", err)
		_ = ls.withLinks(func(f *sessionLinks) bool {
			f.Outbox = append(f.Outbox, outboxNote{To: id, Note: n})
			if len(f.Outbox) > maxOutbox {
				f.Outbox = f.Outbox[len(f.Outbox)-maxOutbox:]
			}
			return true
		})
	}
}

// deliverNote delivers a note once: a retry or a second route finds its id on record.
func (ls *liveSession) deliverNote(n session.SessionNote) (woke, held bool) {
	dup := false
	_ = ls.withLinks(func(f *sessionLinks) bool {
		if n.ID == "" {
			return false
		}
		if dup = slices.Contains(f.Seen, n.ID); dup {
			return false
		}
		f.Seen = append(f.Seen, n.ID)
		if len(f.Seen) > maxSeenNotes {
			f.Seen = f.Seen[len(f.Seen)-maxSeenNotes:]
		}
		return true
	})
	if dup {
		return false, false
	}
	return ls.receiveNote(n)
}

// drainNotes delivers what waited for this session and retries its outbox, in the background: at open and after turns.
func (ls *liveSession) drainNotes() {
	if !ls.links.draining.CompareAndSwap(false, true) {
		return
	}
	ls.mu.Lock()
	if ls.closed {
		ls.mu.Unlock()
		ls.links.draining.Store(false)
		return
	}
	ls.bg.Add(1)
	ls.mu.Unlock()
	go func() {
		defer ls.bg.Done()
		defer ls.links.draining.Store(false)
		ctx, cancel := context.WithTimeout(ls.ctx, 2*sessionSyncTimeout)
		defer cancel()
		for _, n := range takePendingNotes(ls.sess.ID) {
			ls.deliverNote(n)
		}
		if docs := ls.srv.noteDocs(); docs != nil {
			ls.drainNoteDoc(ctx, docs)
		}
		ls.flushOutbox(ctx)
	}()
}

func (ls *liveSession) drainNoteDoc(ctx context.Context, docs sessionNoteDocs) {
	// A fork of another machine's session is that session continued here: it reads a copy of its notes.
	if src := ls.links.forkOf; src != "" {
		if doc, _, err := readNoteDoc(ctx, docs, src); err == nil {
			for _, n := range doc.Notes {
				ls.deliverNote(n)
			}
		}
	}
	doc, cur, err := readNoteDoc(ctx, docs, ls.sess.ID)
	if err != nil {
		ls.srv.log().Debug("session notes: not read", "err", err)
		return
	}
	if cur == nil {
		return
	}
	for _, n := range doc.Notes {
		ls.deliverNote(n)
	}
	// A note added meanwhile keeps the document (a version conflict) and is read next time; ids stop repeats.
	if _, err := docs.DeleteDocument(ctx, &syncv1.DeleteDocumentRequest{Id: cur.GetId(), BaseVersion: proto.Int64(cur.GetVersion())}); err != nil {
		ls.srv.log().Debug("session notes: not cleared", "err", err)
	}
}

func (ls *liveSession) flushOutbox(ctx context.Context) {
	var out []outboxNote
	_ = ls.withLinks(func(f *sessionLinks) bool {
		out, f.Outbox = f.Outbox, nil
		return len(out) > 0
	})
	for _, o := range out {
		if target := ls.srv.liveSession(o.To); target != nil {
			target.deliverNote(o.Note)
			continue
		}
		ls.routeOrKeep(ctx, o.To, o.Note)
	}
}

// DeliverSessionNote takes a note that came through the relay. It goes the way of a note from this machine: a reminder
// and a notice, never a user message. A closed session is reopened for a message, as a message from here reopens it;
// a report waits for it to open.
func (s *Server) DeliverSessionNote(ctx context.Context, req *agentv1.DeliverSessionNoteRequest) error {
	id := req.GetSessionId()
	n, err := noteFromProto(req.GetNote())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if ls := s.liveSession(id); ls != nil {
		ls.deliverNote(n)
		return nil
	}
	if s.acpLive(id) != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on an external agent, which cannot receive messages from other sessions", id))
	}
	if _, path := s.locate(id); path == "" {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", id))
	}
	if n.Kind == noteMessage && !s.stateOf(id).archived {
		ls, err := s.openForNote(ctx, id)
		if err != nil {
			return err
		}
		ls.deliverNote(n)
		return nil
	}
	if err := s.putNoteDoc(ctx, id, n); err != nil {
		return appendPendingNote(id, n)
	}
	return nil
}

func noteToProto(n session.SessionNote) *agentv1.SessionNote {
	kind := agentv1.SessionNote_KIND_MESSAGE
	if n.Kind == noteReport {
		kind = agentv1.SessionNote_KIND_REPORT
	}
	return &agentv1.SessionNote{Id: n.ID, Kind: kind, FromId: n.FromID, FromName: n.FromName, FromTitle: n.FromTitle, Status: n.Status, Text: n.Text}
}

func noteFromProto(p *agentv1.SessionNote) (session.SessionNote, error) {
	n := session.SessionNote{ID: p.GetId(), FromID: p.GetFromId(), FromName: p.GetFromName(), FromTitle: p.GetFromTitle(), Status: p.GetStatus(), Text: p.GetText()}
	switch p.GetKind() {
	case agentv1.SessionNote_KIND_MESSAGE:
		n.Kind = noteMessage
	case agentv1.SessionNote_KIND_REPORT:
		n.Kind = noteReport
	default:
		return n, errors.New("note kind is required")
	}
	if n.ID == "" || n.FromID == "" {
		return n, errors.New("note id and from_id are required")
	}
	if len(n.Text) > maxSessionMessage {
		return n, fmt.Errorf("the note is longer than %d bytes", maxSessionMessage)
	}
	return n, nil
}
