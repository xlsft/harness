package server

import (
	"context"
	"os"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/permissions"
)

// The offer to import a folder's Claude Code files, and the import. Everything goes through package importer, under the same
// rules as `harness-client import --project`. Dismissals stay on this device.

func projectOfferStore() importer.OfferStore {
	return importer.OfferStore{Path: filepath.Join(config.Home(), "project-import.json")}
}

func (s *Server) folderTrusted(path string) bool {
	if s.Base.Trusted != nil {
		return *s.Base.Trusted
	}
	return permissions.IsTrusted(path)
}

func projectItemsToProto(items []importer.ProjectItem) []*agentv1.ProjectImportItem {
	out := make([]*agentv1.ProjectImportItem, 0, len(items))
	for _, it := range items {
		out = append(out, &agentv1.ProjectImportItem{Kind: it.Kind, Source: it.Source, Target: it.Target, Status: importStatus(it.Status), Note: it.Note, NeedsTrust: it.NeedsTrust})
	}
	return out
}

// ScanProjectImport implements AgentService.ScanProjectImport. It writes nothing.
func (s *Server) ScanProjectImport(ctx context.Context, req *connect.Request[agentv1.ScanProjectImportRequest]) (*connect.Response[agentv1.ScanProjectImportResponse], error) {
	path, err := absDir(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	if config.IsChatDir(path) {
		return connect.NewResponse(&agentv1.ScanProjectImportResponse{Root: path}), nil // a folderless session's own directory
	}
	home, _ := os.UserHomeDir()
	root := importer.ProjectRoot(path, home)
	trusted := s.folderTrusted(root)
	rep, err := importer.RunProject(ctx, importer.ProjectOptions{Root: root, Home: home, Trusted: trusted, DryRun: true})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	dismissed := projectOfferStore().Dismissed(root)
	return connect.NewResponse(&agentv1.ScanProjectImportResponse{
		Root: root, Items: projectItemsToProto(rep.Items), Dismissed: dismissed, Trusted: trusted,
		Offer: rep.Planned() > 0 && !dismissed && !importer.HasHarnessFolder(root),
	}), nil
}

// RunProjectImport implements AgentService.RunProjectImport.
func (s *Server) RunProjectImport(ctx context.Context, req *connect.Request[agentv1.RunProjectImportRequest]) (*connect.Response[agentv1.RunProjectImportResponse], error) {
	path, err := absDir(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	root := importer.ProjectRoot(path, home)
	rep, err := importer.RunProject(ctx, importer.ProjectOptions{Root: root, Home: home, Trusted: s.folderTrusted(root), Force: req.Msg.GetForce()})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res := &agentv1.RunProjectImportResponse{Root: root, Items: projectItemsToProto(rep.Items)}
	for _, it := range rep.Items {
		switch it.Status {
		case importer.StatusCopied:
			res.Imported++
		case importer.StatusExists:
			res.Exists++
		case importer.StatusError:
			res.Errors++
		default:
			res.Skipped++
		}
	}
	return connect.NewResponse(res), nil
}

// DismissProjectImport implements AgentService.DismissProjectImport.
func (s *Server) DismissProjectImport(ctx context.Context, req *connect.Request[agentv1.DismissProjectImportRequest]) (*connect.Response[agentv1.DismissProjectImportResponse], error) {
	path, err := absDir(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	if err := projectOfferStore().SetDismissed(importer.ProjectRoot(path, home), req.Msg.GetDismissed()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.DismissProjectImportResponse{}), nil
}
