package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/importer/sources"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// Settings > Import for every tool: the import engine (package importer, readers in importer/sources) behind
// AgentService ListImportSources, ScanImportSource and RunImportSource. The two Claude Code imports keep their own
// screens and RPCs (import.go, project_import.go); they are listed here with `screen` set so one list shows them all.

// importEnv is where the readers look on this machine.
func importerEnv() (importer.Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return importer.Env{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no home directory: %w", err))
	}
	return importer.Env{Home: home, GOOS: runtime.GOOS, AppData: os.Getenv("APPDATA"), ConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		CodexHome: os.Getenv("CODEX_HOME"), CopilotHome: os.Getenv("COPILOT_HOME")}, nil
}

// importMCPTarget is where imported MCP servers go: this device's own list.
func (s *Server) importMCPTarget() importer.MCPTarget {
	return mcp.ImportTarget{Inv: s.mcpInventory()}
}

// importDocExists asks the server whether a document exists; nil without a server.
func (s *Server) importDocExists() func(context.Context, *syncv1.DocumentRef) (bool, error) {
	if s.ImportTo == nil {
		return nil
	}
	return func(ctx context.Context, ref *syncv1.DocumentRef) (bool, error) {
		_, err := s.ImportTo.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: ref}})
		if syncclient.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	}
}

// ListImportSources implements AgentService.ListImportSources.
func (s *Server) ListImportSources(ctx context.Context, _ *connect.Request[agentv1.ListImportSourcesRequest]) (*connect.Response[agentv1.ListImportSourcesResponse], error) {
	env, err := importerEnv()
	if err != nil {
		return nil, err
	}
	claudeDir := filepath.Join(env.Home, ".claude")
	_, statErr := os.Stat(claudeDir)
	res := &agentv1.ListImportSourcesResponse{Connected: s.ImportTo != nil, Sources: []*agentv1.ImportSourceInfo{
		{Id: "claude-code", Name: "Claude Code", Logo: "claude", UserLevel: true, Detected: statErr == nil, Screen: "claude-code",
			Description: "Memory, rules, settings, skills, agents, commands, output styles, workflows, MCP servers and sessions on this computer."},
		{Id: "claude-project", Name: "A folder's Claude Code files", Logo: "claude", FolderLevel: true, Screen: "claude-project",
			Description: "CLAUDE.md and what is under .claude, into the folder's AGENTS.md and .harness."},
	}}
	for _, src := range sources.All() {
		info := src.Info()
		res.Sources = append(res.Sources, &agentv1.ImportSourceInfo{Id: info.ID, Name: info.Name, Description: info.Description, Logo: info.Logo,
			UserLevel: info.User, FolderLevel: info.Folder, Detected: info.User && src.Detect(env)})
	}
	return connect.NewResponse(res), nil
}

func importSource(id string) (importer.Source, error) {
	src := sources.Get(id)
	if src == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no import source %q", id))
	}
	return src, nil
}

func (s *Server) engineOptions(env importer.Env) importer.EngineOptions {
	o := importer.EngineOptions{Env: env, Exists: s.importDocExists(), MCP: s.importMCPTarget(), Trusted: s.folderTrusted,
		ForceHint: importForceHint, SecretsHint: importSecretsHint}
	if s.ImportTo != nil {
		o.Putter = s.ImportTo
	}
	// Sessions of other tools land where this client's sessions are, and go up like the Claude Code import's.
	o.ProjectsDir, o.HoldSession, o.SessionDone = config.ProjectsDir(), s.holdSession, sessionUploaded
	if s.ImportTo != nil {
		host, _ := os.Hostname()
		o.Queue = syncclient.NewUploader(syncclient.UploaderOptions{API: s.ImportTo, DeviceID: s.DeviceID, DeviceName: host, Logger: s.log(), Objects: syncclient.SharedObjectQueue(s.ImportTo, s.log())})
		o.UploadSessions = true
	}
	return o
}

func sourceItemToProto(it importer.SourceItem) *agentv1.ImportSourceItem {
	return &agentv1.ImportSourceItem{Id: it.ID, Section: it.Section, Kind: string(it.Kind), Name: it.Name, Source: it.Source, Target: it.Target,
		Status: importStatus(it.Status), Note: it.Note, NeedsTrust: it.NeedsTrust, HasSecrets: it.HasSecrets}
}

// importFolder resolves a request's folder; "" stays user level.
func importFolder(folder string) (string, error) {
	if folder == "" {
		return "", nil
	}
	return absDir(folder)
}

// ScanImportSource implements AgentService.ScanImportSource. It writes nothing.
func (s *Server) ScanImportSource(ctx context.Context, req *connect.Request[agentv1.ScanImportSourceRequest]) (*connect.Response[agentv1.ScanImportSourceResponse], error) {
	src, err := importSource(req.Msg.GetSourceId())
	if err != nil {
		return nil, err
	}
	folder, err := importFolder(req.Msg.GetFolder())
	if err != nil {
		return nil, err
	}
	env, err := importerEnv()
	if err != nil {
		return nil, err
	}
	if folder == "" {
		s.refreshImportListing(ctx)
	}
	scan, err := importer.ScanSource(ctx, src, folder, s.engineOptions(env))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	res := &agentv1.ScanImportSourceResponse{SourceId: src.Info().ID, Root: scan.Root, Found: scan.Found, Connected: s.ImportTo != nil,
		ServerChecked: scan.ServerChecked, Trusted: scan.Trusted}
	for _, it := range scan.Items {
		res.Items = append(res.Items, sourceItemToProto(it))
	}
	return connect.NewResponse(res), nil
}

// RunImportSource implements AgentService.RunImportSource. Cancelling the call stops the run at the next item.
func (s *Server) RunImportSource(ctx context.Context, req *connect.Request[agentv1.RunImportSourceRequest], stream *connect.ServerStream[agentv1.RunImportSourceResponse]) error {
	m := req.Msg
	src, err := importSource(m.GetSourceId())
	if err != nil {
		return err
	}
	if len(m.GetItemIds()) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("nothing was chosen to import"))
	}
	folder, err := importFolder(m.GetFolder())
	if err != nil {
		return err
	}
	env, err := importerEnv()
	if err != nil {
		return err
	}
	if !s.importing.CompareAndSwap(false, true) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("an import is already running"))
	}
	defer s.importing.Store(false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()
	s.forgetSkillDocs()
	defer s.forgetSkillDocs()

	if folder == "" {
		s.refreshImportListing(ctx)
	}
	o := s.engineOptions(env)
	o.IncludeSecrets, o.Force = m.GetIncludeSecrets(), m.GetForce()
	o.OnProgress = func(p importer.SourceProgress) {
		pr := &agentv1.ImportSourceProgress{Current: p.Current, Done: int32(p.Done), Total: int32(p.Total)}
		if p.Item != nil {
			pr.Item = sourceItemToProto(*p.Item)
		}
		if stream.Send(&agentv1.RunImportSourceResponse{Event: &agentv1.RunImportSourceResponse_Progress{Progress: pr}}) != nil {
			cancel()
		}
	}
	rep, runErr := importer.RunSource(ctx, src, folder, m.GetItemIds(), o)
	if ctx.Err() != nil {
		return connect.NewError(connect.CodeCanceled, errors.New("import cancelled"))
	}
	report := &agentv1.ImportSourceReport{}
	if runErr != nil {
		report.Error = runErr.Error()
	}
	if rep != nil {
		for _, it := range rep.Items {
			pb := sourceItemToProto(it)
			report.Items = append(report.Items, pb)
			switch pb.Status {
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_IMPORTED:
				report.Imported++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_EXISTS:
				report.Exists++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_MANUAL:
				report.Manual++
			case agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_ERROR:
				report.Errors++
			default:
				report.Skipped++
			}
		}
	}
	return stream.Send(&agentv1.RunImportSourceResponse{Event: &agentv1.RunImportSourceResponse_Report{Report: report}})
}
