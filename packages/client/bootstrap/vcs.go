package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/lsp"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/vcs"
)

// startVCS runs the gh/glab device providers for the session's directory in the background. It runs
// only when the session is synced with a server (startSync succeeded) and HARNESS_NO_VCS is not 1;
// the returned cancel func stops it (Session.Close).
func startVCS(s *Session, url string, hc *http.Client, tokens auth.TokenSource, log *slog.Logger) (*vcs.Watcher, context.CancelFunc) {
	if s.Sync == nil || url == "" || tokens == nil || os.Getenv("HARNESS_NO_VCS") == "1" {
		return nil, nil
	}
	w := vcs.NewWatcher(vcs.Options{Reporter: vcs.NewClient(url, hc, tokens), Dir: s.Cwd, Logger: log})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = w.Run(ctx) }()
	return w, cancel
}

// PRStateDir holds the sessions' pull request bindings (device-local).
func PRStateDir() string { return filepath.Join(config.Home(), "pr-watch") }

// registerPRTools creates the session's pull request tracker and its tools.
func (s *Session) registerPRTools(o Options, log *slog.Logger) {
	s.PR = vcs.NewTracker(vcs.TrackerOptions{
		SessionID: s.ID, Dir: s.Cwd, StateDir: PRStateDir(), Disabled: os.Getenv("HARNESS_NO_VCS") == "1",
		Providers: o.VCSProviders, Schedule: o.VCSSchedule, Logger: log, WakesPerHour: s.prWakesPerHour,
		SaltFile: filepath.Join(config.Home(), "pr-marker-salt"),
	})
	for _, t := range vcs.PRTools(s.PR) {
		s.Tools.Register(t)
	}
}

// prWakesPerHour reads the merged settings of the session's folder again, so a change of the user's synced settings
// document (UpdateUserPreferences) applies without reopening the session.
func (s *Session) prWakesPerHour() int {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Cwd, Docs: docs, Trusted: s.Trusted})
	if err != nil {
		return settings.DefaultPRMonitorWakesPerHour
	}
	return st.PRMonitorWakesPerHour
}

// subagentLimits reads the merged settings of the session's folder again, so a change of the user's synced settings
// document applies to the next agent started.
func (s *Session) subagentLimits() (depth, running int) {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Cwd, Docs: docs, Trusted: s.Trusted})
	if err != nil {
		return settings.DefaultSubagentMaxDepth, settings.DefaultSubagentMaxRunning
	}
	return st.SubagentMaxDepth, st.SubagentMaxRunning
}

// workflowMaxAgents reads the hard cap on agent() calls per run, like subagentLimits.
func (s *Session) workflowMaxAgents() int {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Dir(), Docs: docs, Trusted: s.Trusted})
	if err != nil {
		return settings.DefaultWorkflowMaxAgents
	}
	return st.WorkflowMaxAgents
}

// lspConfig reads the language server settings again, so that the user's switches apply to a session that runs.
func (s *Session) lspConfig() lsp.Config {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Dir(), Docs: docs, Trusted: s.IsTrusted()})
	if err != nil {
		return lsp.Config{Enabled: true, Diagnostics: true}
	}
	return lsp.Config{Enabled: st.LSPEnabled, Diagnostics: st.LSPDiagnostics, StartForDiagnostics: st.LSPDiagnosticsStartServer, Servers: st.LSPServers}
}

// lspTrusted reports whether a language server may start for a working directory: one inside the session's trusted
// folder, or one trusted by itself (a directory the session gained does not inherit the folder's trust).
func (s *Session) lspTrusted(dir string) bool {
	if s.IsTrusted() {
		cwd := s.Dir()
		for _, c := range []string{cwd, realDir(cwd)} {
			if rel, err := filepath.Rel(c, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return true
			}
		}
	}
	return folderTrust(s.open, dir)
}

func realDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ConnectPR sends what the tracker sees to the session: notes as reminders, a state change to onChange.
func (s *Session) ConnectPR(onChange func()) {
	s.PR.SetHooks(vcs.TrackerHooks{
		OnChange: onChange,
		OnNote: func(n vcs.PRNote) {
			s.Agent.NotePRActivity(n.Number, n.URL, n.Events, n.External, n.Marker, n.Wake)
		},
		OnDetected: func(number int64, url string) {
			s.Agent.NotePRDetected(number, url, s.PR.Marker())
			if s.VCS != nil {
				s.VCS.Poke()
			}
		},
	})
}

// watchPRCommands shows the tracker every finished shell command, so one that opens a PR is noticed without polling.
func (s *Session) watchPRCommands(next func(agent.Event)) func(agent.Event) {
	return func(e agent.Event) {
		if next != nil {
			next(e)
		}
		if e.Kind == agent.EventToolEnd && e.ToolName == "Bash" && !e.IsError {
			s.PR.ObserveCommand(tools.BashCommand(e.Input))
		}
	}
}
