package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
)

// Approvals stores this device's decisions about stdio servers, bound to the config hash: a changed
// config needs a new decision. The server can never approve a launch by itself; this file is the
// local source of truth and is reported to the server with ReportDeviceStatus.
type Approvals struct {
	// Path of the JSON file (e.g. ~/.harness/mcp-approvals.json).
	Path string

	mu sync.Mutex
}

type approvalFile struct {
	Servers map[string]approvalEntry `json:"servers"`
}

type approvalEntry struct {
	Name       string `json:"name,omitempty"`
	ConfigHash string `json:"config_hash"`
	Approved   bool   `json:"approved"`
	At         string `json:"at"`
}

func (a *Approvals) load() approvalFile {
	f := approvalFile{Servers: map[string]approvalEntry{}}
	if a == nil || a.Path == "" {
		return f
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return f
	}
	_ = json.Unmarshal(data, &f)
	if f.Servers == nil {
		f.Servers = map[string]approvalEntry{}
	}
	return f
}

// State returns the decision for the server's current config hash (PENDING when there is none).
func (a *Approvals) State(serverID, hash string) mcpv1.ApprovalState {
	if a == nil {
		return mcpv1.ApprovalState_APPROVAL_STATE_PENDING
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.load().Servers[serverID]
	switch {
	case !ok || e.ConfigHash != hash:
		return mcpv1.ApprovalState_APPROVAL_STATE_PENDING
	case e.Approved:
		return mcpv1.ApprovalState_APPROVAL_STATE_APPROVED
	default:
		return mcpv1.ApprovalState_APPROVAL_STATE_DENIED
	}
}

// Set records a decision.
func (a *Approvals) Set(serverID, name, hash string, approved bool) error {
	if a == nil || a.Path == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.load()
	f.Servers[serverID] = approvalEntry{Name: name, ConfigHash: hash, Approved: approved, At: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o700); err != nil {
		return err
	}
	tmp := a.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.Path)
}

// Forget drops the decision about a deleted server.
func (a *Approvals) Forget(serverID string) error {
	if a == nil || a.Path == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.load()
	if _, ok := f.Servers[serverID]; !ok {
		return nil
	}
	delete(f.Servers, serverID)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.Path, data, 0o600)
}
