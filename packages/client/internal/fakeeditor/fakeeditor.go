// Package fakeeditor is an editor extension for tests: it serves harness.ide.v1.EditorBridgeService on loopback and
// announces itself with a lock file, as packages/vscode does.
package fakeeditor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"connectrpc.com/connect"

	idev1 "github.com/xlsft/harness/packages/client/gen/harness/ide/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/ide/v1/idev1connect"
)

// Token is the bearer token every Editor expects.
const Token = "fake-editor-token-0123456789"

// Editor is the fake. Set the fields before the first call reaches it.
type Editor struct {
	idev1connect.UnimplementedEditorBridgeServiceHandler

	// Answer decides a diff; nil waits until the call is cancelled.
	Answer func(*idev1.ShowDiffRequest) *idev1.ShowDiffResponse
	// Problems are what GetDiagnostics returns.
	Problems []*idev1.Diagnostic
	// Buffers are the files "open" in the editor.
	Buffers map[string]string
	// LockPath is the lock file written.
	LockPath string

	// Diffs receives every diff shown; Cancelled every diff whose call was cancelled.
	Diffs     chan *idev1.ShowDiffRequest
	Cancelled chan *idev1.ShowDiffRequest
	// Applied receives the requests of EditApplied; Opened those of OpenFile.
	Applied chan *idev1.EditAppliedRequest
	Opened  chan *idev1.OpenFileRequest

	mu     sync.Mutex
	folder []string
}

// Start serves the fake and writes its lock file under $HARNESS_HOME/ide, for folders.
func Start(t *testing.T, folders ...string) *Editor {
	t.Helper()
	e := &Editor{
		Buffers: map[string]string{}, folder: folders,
		Diffs: make(chan *idev1.ShowDiffRequest, 16), Cancelled: make(chan *idev1.ShowDiffRequest, 16),
		Applied: make(chan *idev1.EditAppliedRequest, 64), Opened: make(chan *idev1.OpenFileRequest, 16),
	}
	mux := http.NewServeMux()
	path, h := idev1connect.NewEditorBridgeServiceHandler(e)
	mux.Handle(path, h)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+Token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	port := ln.Addr().(*net.TCPAddr).Port
	dir := filepath.Join(os.Getenv("HARNESS_HOME"), "ide")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e.LockPath = filepath.Join(dir, strconv.Itoa(os.Getpid())+"-"+strconv.Itoa(port)+".json")
	data, err := json.Marshal(map[string]any{"port": port, "token": Token, "pid": os.Getpid(), "name": "Fake Editor", "folders": folders})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.LockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *Editor) Ping(context.Context, *connect.Request[idev1.PingRequest]) (*connect.Response[idev1.PingResponse], error) {
	return connect.NewResponse(&idev1.PingResponse{Name: "Fake Editor", Version: "1", WorkspaceFolders: e.folder}), nil
}

func (e *Editor) ShowDiff(ctx context.Context, req *connect.Request[idev1.ShowDiffRequest]) (*connect.Response[idev1.ShowDiffResponse], error) {
	e.Diffs <- req.Msg
	if e.Answer != nil {
		if a := e.Answer(req.Msg); a != nil {
			return connect.NewResponse(a), nil
		}
	}
	<-ctx.Done()
	e.Cancelled <- req.Msg
	return nil, ctx.Err()
}

func (e *Editor) GetDiagnostics(_ context.Context, req *connect.Request[idev1.GetDiagnosticsRequest]) (*connect.Response[idev1.GetDiagnosticsResponse], error) {
	var out []*idev1.Diagnostic
	for _, d := range e.Problems {
		if len(req.Msg.GetPaths()) == 0 {
			out = append(out, d)
			continue
		}
		for _, p := range req.Msg.GetPaths() {
			if p == d.GetPath() {
				out = append(out, d)
			}
		}
	}
	return connect.NewResponse(&idev1.GetDiagnosticsResponse{Diagnostics: out}), nil
}

func (e *Editor) OpenFile(_ context.Context, req *connect.Request[idev1.OpenFileRequest]) (*connect.Response[idev1.OpenFileResponse], error) {
	e.Opened <- req.Msg
	return connect.NewResponse(&idev1.OpenFileResponse{}), nil
}

func (e *Editor) ReadBuffer(_ context.Context, req *connect.Request[idev1.ReadBufferRequest]) (*connect.Response[idev1.ReadBufferResponse], error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	text, open := e.Buffers[req.Msg.GetPath()]
	return connect.NewResponse(&idev1.ReadBufferResponse{Open: open, Content: text, Dirty: open}), nil
}

func (e *Editor) EditApplied(_ context.Context, req *connect.Request[idev1.EditAppliedRequest]) (*connect.Response[idev1.EditAppliedResponse], error) {
	e.Applied <- req.Msg
	return connect.NewResponse(&idev1.EditAppliedResponse{}), nil
}
