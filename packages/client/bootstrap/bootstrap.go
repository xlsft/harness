// Package bootstrap wires the client together for one session: credentials,
// gateway and prompt adapters, model selection, the frozen prompt bundle,
// workspace context, tools, permissions, transcript and the agent. Frontends
// (headless SDK mode, the TUI in packages/cli) start here.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Options configure a session.
type Options struct {
	Cwd            string
	AdditionalDirs []string

	// Server overrides; when Gateway/Prompts are nil the HTTPS adapters are
	// created from stored credentials (or HARNESS_SERVER_URL + HARNESS_ACCESS_TOKEN).
	ServerURL string
	Gateway   gateway.Gateway
	Prompts   prompts.Source
	HTTP      *http.Client

	Model           string
	Effort          string
	MaxTurns        int
	PermissionMode  permissions.Mode
	AllowedTools    []string
	DisallowedTools []string

	Continue      bool
	Resume        string
	SessionID     string
	NoPersistence bool
	Entrypoint    string

	OnEvent func(agent.Event)
	Logger  *slog.Logger
}

// Session is a ready-to-run session.
type Session struct {
	ID          string
	Cwd         string
	Model       string
	Catalog     *llm.Catalog
	Bundle      *prompts.Bundle
	Agent       *agent.Agent
	Permissions *permissions.Engine
	Tools       *tools.Registry
	ToolEnv     *tools.Env
	Transcript  *session.Writer
	Gateway     gateway.Gateway
	User        *auth.User
	Resumed     bool
}

// Close releases resources.
func (s *Session) Close() error {
	if s.Transcript != nil {
		return s.Transcript.Close()
	}
	return nil
}

// ToolNames returns the tools offered to the model.
func (s *Session) ToolNames() []string {
	var out []string
	for _, n := range s.Tools.Names() {
		if _, ok := s.Bundle.Tools[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// Tokens resolves a token source and server URL from the environment or the
// credential store.
func Tokens(serverOverride string) (auth.TokenSource, string, *auth.User, error) {
	if t := os.Getenv("HARNESS_ACCESS_TOKEN"); t != "" {
		url := serverOverride
		if url == "" {
			url = os.Getenv("HARNESS_SERVER_URL")
		}
		if url == "" {
			return nil, "", nil, errors.New("HARNESS_ACCESS_TOKEN requires HARNESS_SERVER_URL")
		}
		return auth.StaticToken(t), url, nil, nil
	}
	store := auth.DefaultStore(config.CredentialsFile())
	creds, err := store.Load()
	if err != nil {
		return nil, "", nil, err
	}
	if creds == nil {
		return nil, "", nil, auth.ErrNotLoggedIn
	}
	url := creds.Server
	if serverOverride != "" {
		url = serverOverride
	}
	m, err := auth.NewManager(auth.NewConnectService(url, nil), store)
	if err != nil {
		return nil, "", nil, err
	}
	u := creds.User
	return m, url, &u, nil
}

// Open builds a session.
func Open(ctx context.Context, o Options) (*Session, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	cwd := o.Cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	cwd, _ = filepath.Abs(cwd)
	s := &Session{Cwd: cwd}

	gw, src := o.Gateway, o.Prompts
	if gw == nil || src == nil {
		tokens, url, user, err := Tokens(o.ServerURL)
		if err != nil {
			return nil, err
		}
		s.User = user
		hc := o.HTTP
		if hc == nil {
			hc = rpc.HTTPClient(url)
		}
		if gw == nil {
			gw = connectgw.New(url, hc, tokens)
		}
		if src == nil {
			src = prompts.NewConnectSource(url, hc, tokens)
		}
	}
	s.Gateway = gw

	// Session identity and history.
	store := session.Store{Root: config.ProjectsDir()}
	var conv session.Conversation
	var path string
	switch {
	case o.Resume != "" || o.Continue:
		id := o.Resume
		if id == "" {
			var err error
			if id, err = store.Latest(cwd); err != nil {
				return nil, err
			}
		}
		p, err := store.Find(cwd, id)
		if err != nil {
			return nil, err
		}
		entries, err := session.Load(p)
		if err != nil {
			return nil, fmt.Errorf("load session: %w", err)
		}
		conv = session.Rebuild(entries)
		s.ID, path, s.Resumed = id, p, true
	default:
		s.ID = o.SessionID
		if s.ID == "" {
			s.ID = session.NewID()
		}
		path = store.Path(cwd, s.ID)
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("session %s already exists; use --resume", s.ID)
		}
	}

	// Model.
	catalog, err := gw.Models(ctx)
	if err != nil {
		log.Warn("model catalog unavailable", "err", err)
	}
	s.Catalog = catalog
	s.Model = o.Model
	if s.Model == "" {
		s.Model = conv.Model
	}
	if s.Model == "" && catalog != nil {
		s.Model = catalog.Slots["main"]
		if s.Model == "" && len(catalog.Models) > 0 {
			s.Model = catalog.Models[0].ID
		}
	}
	if s.Model == "" {
		return nil, errors.New("no model selected: pass --model or configure a main slot on the server")
	}
	info, _ := catalog.Find(s.Model)

	// Prompt bundle, frozen per session.
	loader := &prompts.Loader{Source: src, Dir: config.PromptCacheDir(), Logger: log}
	if s.Resumed && conv.PromptBundleVersion != "" {
		s.Bundle, err = loader.Frozen(ctx, s.Model, conv.PromptBundleVersion)
	} else {
		s.Bundle, err = loader.Get(ctx, s.Model)
	}
	if err != nil {
		return nil, err
	}

	// Workspace context.
	env := workspace.Detect(ctx, cwd)
	env.ModelID, env.ModelDisplayName = s.Model, info.DisplayName
	gitForContext := ""
	if !s.Bundle.UsesPlaceholder("git_status") {
		gitForContext = env.GitStatus
	}
	projectContext := workspace.FormatInstructions(workspace.ProjectInstructions(cwd), gitForContext)

	// Tools and permissions.
	scratch := filepath.Join(config.TempRoot(), session.Slug(cwd), s.ID, "scratchpad")
	s.ToolEnv = tools.NewEnv(cwd, scratch)
	s.ToolEnv.AdditionalDirs = o.AdditionalDirs
	s.ToolEnv.Reminders = func(k string) string { return s.Bundle.Reminder(k, "") }
	s.Tools = tools.Default()
	s.Permissions = permissions.New(permissions.Config{
		Cwd: cwd, AdditionalDirectories: o.AdditionalDirs, DefaultMode: o.PermissionMode,
		Allow: o.AllowedTools, Deny: o.DisallowedTools, ScratchDirs: []string{scratch},
	})
	disabled := map[string]bool{}
	for _, n := range s.Tools.Names() {
		if s.Permissions.IsDisabled(n) {
			disabled[n] = true
		}
	}

	// Transcript.
	branch := ""
	if env.IsGit {
		branch = workspace.GitBranch(ctx, cwd)
	}
	s.Transcript, err = session.OpenWriter(path, session.WriterOptions{
		SessionID: s.ID, Cwd: cwd, GitBranch: branch, Version: config.Version,
		Entrypoint: o.Entrypoint, LastUUID: conv.LastUUID, Disabled: o.NoPersistence,
	})
	if err != nil {
		return nil, err
	}
	if !s.Resumed || conv.PromptBundleVersion != s.Bundle.Version || conv.Model != s.Model {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, PromptBundleVersion: s.Bundle.Version, Model: s.Model})
	}

	maxTokens := 32000
	if info.MaxOutputTokens > 0 && info.MaxOutputTokens < maxTokens {
		maxTokens = info.MaxOutputTokens
	}
	s.Agent = agent.New(agent.Config{
		Gateway: gw, Bundle: s.Bundle, Tools: s.Tools, ToolEnv: s.ToolEnv,
		Permissions: s.Permissions, Transcript: s.Transcript,
		Model: s.Model, ModelDisplayName: info.DisplayName, MaxTokens: maxTokens,
		MaxTurns: o.MaxTurns, Effort: o.Effort, SessionID: s.ID,
		Vars: env.Vars(), ProjectContext: projectContext, History: conv,
		DisabledTools:  disabled,
		PermissionMode: func() string { return string(s.Permissions.Mode()) },
		OnEvent:        o.OnEvent, Logger: log,
	})
	return s, nil
}
