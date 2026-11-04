package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// Import from Claude Code in the app: ScanClaudeImport previews it, RunClaudeImport does it with what the user chose.
// Both go through the importer, under the same rules as `harness-client import claude`. The transcripts are copied to
// HARNESS_HOME/projects, where ListSessions finds them on its next call, so they join the session list without a restart.

// How the importer's notes name this API's switches, for a frontend to show.
const (
	importForceHint   = "Overwrite existing"
	importSecretsHint = "Include secrets"
)

var importSections = map[agentv1.ClaudeImportSection]importer.Section{
	agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MEMORY:        importer.SectionMemory,
	agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_SETTINGS:      importer.SectionSettings,
	agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_SKILLS:        importer.SectionSkills,
	agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_AGENTS:        importer.SectionAgents,
	agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_COMMANDS:      importer.SectionCommands,
	agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_OUTPUT_STYLES: importer.SectionOutputStyles,
	agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MCP_SERVERS:   importer.SectionMCP,
}

// claudePaths returns Claude Code's directory and the state file beside it.
func claudePaths(dir string) (claudeDir, claudeJSON string, err error) {
	if dir != "" {
		dir = filepath.Clean(dir)
		return dir, filepath.Join(filepath.Dir(dir), ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no home directory: %w", err))
	}
	return filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"), nil
}

// holdSession says why a session must stay as it is: it is open here, or the server knows it from another machine.
func (s *Server) holdSession(id string) string {
	s.mu.Lock()
	live := s.live[id] != nil
	s.mu.Unlock()
	switch {
	case live:
		return "open in harness right now"
	case s.remoteInfo(id) != nil:
		return "on another machine"
	}
	return ""
}

// refreshImportListing brings the server's session list up to date for holdSession; a server that does not answer
// leaves the last one.
func (s *Server) refreshImportListing(ctx context.Context) {
	if s.Sync != nil {
		s.refreshStates(ctx, "")
	}
}

func sessionUploaded(id string) bool { return syncclient.Uploaded("", id) }

// ScanClaudeImport implements AgentService.ScanClaudeImport. It writes nothing.
func (s *Server) ScanClaudeImport(ctx context.Context, req *connect.Request[agentv1.ScanClaudeImportRequest]) (*connect.Response[agentv1.ScanClaudeImportResponse], error) {
	dir, cj, err := claudePaths(req.Msg.GetClaudeDir())
	if err != nil {
		return nil, err
	}
	o := importer.ScanOptions{ClaudeHome: dir, ClaudeJSON: cj, ProjectsDir: config.ProjectsDir(),
		SecretsHint: importSecretsHint, HoldSession: s.holdSession, SessionDone: sessionUploaded}
	if s.ImportTo != nil {
		o.Exists = func(ctx context.Context, ref *syncv1.DocumentRef) (bool, error) {
			_, err := s.ImportTo.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: ref}})
			if syncclient.IsNotFound(err) {
				return false, nil
			}
			return err == nil, err
		}
	}
	s.refreshImportListing(ctx)
	scan, err := importer.Scan(ctx, o)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res := &agentv1.ScanClaudeImportResponse{Found: scan.Found, ClaudeDir: dir, Connected: s.ImportTo != nil, ServerChecked: scan.ServerChecked}
	for _, it := range scan.Items {
		res.Items = append(res.Items, importItemToProto(it, it.Key))
	}
	for _, p := range scan.Projects {
		res.Projects = append(res.Projects, importProjectToProto(p))
	}
	return connect.NewResponse(res), nil
}

func importProjectToProto(p importer.Project) *agentv1.ClaudeImportProject {
	out := &agentv1.ClaudeImportProject{Folder: p.Folder, Path: p.Path, SessionCount: int32(len(p.Sessions)), ImportedCount: int32(p.Imported),
		TotalBytes: p.Bytes, NewestAt: importTime(p.Newest)}
	for _, ss := range p.Sessions {
		out.Sessions = append(out.Sessions, &agentv1.ClaudeImportSession{Id: ss.ID, Title: ss.Title, SizeBytes: ss.Bytes,
			UpdatedAt: importTime(ss.Modified), Status: importStatus(ss.Status), Note: ss.Note})
	}
	return out
}

func importTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// importStatus maps the importer's words. What a run planned and did not reach is not imported (it was cut short); a
// scan's unreadable item is skipped until a run reports its error.
func importStatus(st importer.Status) agentv1.ClaudeImportStatus {
	switch st {
	case importer.StatusPlanned:
		return agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_NEW
	case importer.StatusExists:
		return agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_EXISTS
	case importer.StatusUploaded, importer.StatusCopied:
		return agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_IMPORTED
	case importer.StatusQueued:
		return agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_QUEUED
	case importer.StatusNoted:
		return agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_MANUAL
	case importer.StatusError:
		return agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_ERROR
	}
	return agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_SKIPPED
}

func importItemToProto(it importer.Item, name string) *agentv1.ClaudeImportItem {
	out := &agentv1.ClaudeImportItem{Section: agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_SESSIONS, Kind: it.Kind, Name: name, Status: importStatus(it.Status), Note: it.Note}
	if want := importer.SectionOf(it.Kind); want != "" {
		for pb, sec := range importSections {
			if sec == want {
				out.Section = pb
			}
		}
	}
	return out
}

// RunClaudeImport implements AgentService.RunClaudeImport. Cancelling the call stops the run at the next item (or within
// the copy under way); what is done stays done, and a run after it goes on from there.
func (s *Server) RunClaudeImport(ctx context.Context, req *connect.Request[agentv1.RunClaudeImportRequest], stream *connect.ServerStream[agentv1.RunClaudeImportResponse]) error {
	if s.ImportTo == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("importing needs a harness server; this client is not connected to one"))
	}
	m := req.Msg
	sections := map[importer.Section]bool{}
	for _, pb := range m.GetSections() {
		if sec, ok := importSections[pb]; ok {
			sections[sec] = true
		}
	}
	if len(sections) == 0 && len(m.GetProjectPaths()) == 0 && len(m.GetSessionIds()) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("nothing was chosen to import"))
	}
	dir, cj, err := claudePaths(m.GetClaudeDir())
	if err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no Claude Code directory at %s", dir))
	}
	if !s.importing.CompareAndSwap(false, true) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("an import is already running"))
	}
	defer s.importing.Store(false)
	// The run ends with the call or with this client.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()

	s.refreshImportListing(ctx)
	host, _ := os.Hostname()
	o := importer.Options{
		ClaudeHome: dir, ClaudeJSON: cj, ProjectsDir: config.ProjectsDir(), Sections: sections,
		IncludeSecrets: m.GetIncludeSecrets(), Force: m.GetForce(), Putter: s.ImportTo,
		Queue:          syncclient.NewUploader(syncclient.UploaderOptions{API: s.ImportTo, DeviceID: s.DeviceID, DeviceName: host, Logger: s.log(), Objects: syncclient.SharedObjectQueue(s.ImportTo, s.log())}),
		UploadSessions: true, HoldSession: s.holdSession, SessionDone: sessionUploaded,
		ForceHint: importForceHint, SecretsHint: importSecretsHint,
	}
	// What the sessions are called, and which folders the chosen projects are, come from the same walk a scan does.
	labels := map[string]string{}
	if len(m.GetProjectPaths())+len(m.GetSessionIds()) > 0 {
		paths, ids := stringSet(m.GetProjectPaths()), stringSet(m.GetSessionIds())
		folders := map[string]bool{}
		for _, p := range importer.ScanSessions(ctx, importer.ScanOptions{ClaudeHome: dir, ProjectsDir: o.ProjectsDir}) {
			if paths[p.Path] {
				folders[p.Folder] = true
			}
			for _, ss := range p.Sessions {
				labels[p.Folder+"/"+ss.ID+".jsonl"] = firstNonEmptyString(ss.Title, ss.ID)
			}
		}
		o.Sessions = true
		o.SelectSession = func(folder, id string) bool { return folders[folder] || ids[id] }
	}
	name := func(it string) string {
		if l, ok := labels[it]; ok {
			return l
		}
		return it
	}
	report := &agentv1.ClaudeImportReport{}
	o.OnProgress = func(p importer.Progress) {
		pr := &agentv1.ClaudeImportProgress{Current: p.Kind + " " + name(p.Key), Done: int32(p.Done), Total: int32(p.Total)}
		if p.Item != nil {
			pr.Item = importItemToProto(*p.Item, name(p.Item.Key))
		}
		if stream.Send(&agentv1.RunClaudeImportResponse{Event: &agentv1.RunClaudeImportResponse_Progress{Progress: pr}}) != nil {
			cancel() // the frontend is gone
		}
	}

	rep, runErr := importer.Run(ctx, o)
	if ctx.Err() != nil {
		return connect.NewError(connect.CodeCanceled, errors.New("import cancelled"))
	}
	if runErr != nil {
		report.Error = runErr.Error()
	}
	if rep != nil {
		for _, it := range rep.Items {
			if it.Kind == "session" && it.Status == importer.StatusNoted {
				continue // the count of subagent transcripts, for the CLI's table
			}
			pb := importItemToProto(it, name(it.Key))
			if it.Status == importer.StatusPlanned {
				pb.Status, pb.Note = agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_SKIPPED, "not reached: the run stopped before it"
			}
			report.Items = append(report.Items, pb)
			switch pb.Status {
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_IMPORTED:
				report.Imported++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_EXISTS:
				report.Exists++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_SKIPPED:
				report.Skipped++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_QUEUED:
				report.Queued++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_MANUAL:
				report.Manual++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_ERROR:
				report.Errors++
			}
		}
	}
	return stream.Send(&agentv1.RunClaudeImportResponse{Event: &agentv1.RunClaudeImportResponse_Report{Report: report}})
}

func stringSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" {
			out[v] = true
		}
	}
	return out
}

func firstNonEmptyString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
