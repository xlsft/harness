package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

// GetWorkspaceTrust reports whether path is trusted and what trusting it would enable, so frontends
// can show the trust dialog before the first session in a folder.
func (s *Server) GetWorkspaceTrust(ctx context.Context, req *connect.Request[agentv1.GetWorkspaceTrustRequest]) (*connect.Response[agentv1.GetWorkspaceTrustResponse], error) {
	path, err := absDir(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	if config.IsChatDir(path) {
		// The private directory of a folderless session is always trusted (bootstrap.Options.NoFolder).
		return connect.NewResponse(&agentv1.GetWorkspaceTrustResponse{Trusted: true, TrustedRoot: path}), nil
	}
	root := permissions.DefaultTrustStore().TrustedRoot(path)
	repo := permissions.RepoRoot(path)
	resp := &agentv1.GetWorkspaceTrustResponse{Trusted: root != "", TrustedRoot: root}
	resp.HasProjectMcpServers = fileExists(filepath.Join(repo, ".mcp.json"))
	for _, dir := range []string{".claude", ".harness"} {
		hooks, allow := committedSettings(filepath.Join(repo, dir, "settings.json"))
		resp.HasProjectHooks = resp.HasProjectHooks || hooks
		resp.HasCommittedAllowRules = resp.HasCommittedAllowRules || allow
	}
	return connect.NewResponse(resp), nil
}

// SetWorkspaceTrust trusts or untrusts path on this machine.
func (s *Server) SetWorkspaceTrust(ctx context.Context, req *connect.Request[agentv1.SetWorkspaceTrustRequest]) (*connect.Response[agentv1.SetWorkspaceTrustResponse], error) {
	path, err := absDir(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	store := permissions.DefaultTrustStore()
	if req.Msg.GetTrusted() {
		err = store.Trust(path)
	} else {
		err = store.Untrust(path)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.SetWorkspaceTrustResponse{}), nil
}

func absDir(p string) (string, error) {
	if p == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("path is required"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("path is not a directory"))
	}
	return abs, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// committedSettings reports whether a committed settings file defines hooks or allow rules.
func committedSettings(path string) (hooks, allow bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var f struct {
		Hooks       map[string]json.RawMessage `json:"hooks"`
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if json.Unmarshal(data, &f) != nil {
		return false, false
	}
	return len(f.Hooks) > 0, len(f.Permissions.Allow) > 0
}
