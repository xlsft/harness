package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/workspace"
)

// startSync opens the sync client (cache + watch loop) and the session uploader. It never fails the
// session: without a server or with HARNESS_NO_SYNC=1 it returns nils and the session runs on local
// files only.
func startSync(ctx context.Context, o Options, url string, hc *http.Client, tokens auth.TokenSource, user *auth.User, log *slog.Logger) (*syncclient.Syncer, *syncclient.Uploader, context.CancelFunc) {
	if o.NoSync || os.Getenv("HARNESS_NO_SYNC") == "1" || url == "" || tokens == nil {
		return nil, nil, nil
	}
	identity := url
	if user != nil && user.ID != "" {
		identity += "|" + user.ID
	}
	deviceID := deviceID()
	syncer, err := syncclient.Open(ctx, syncclient.OpenOptions{
		ServerURL: url, HTTP: hc, Tokens: tokens, Identity: identity, Source: "client:" + deviceID, Logger: log,
	})
	if err != nil {
		log.Warn("sync unavailable, using local files only", "err", err)
		return nil, nil, nil
	}
	// The watch loop and uploader outlive Open's ctx; Session.Close cancels them.
	runCtx, cancel := context.WithCancel(context.Background())
	go func() { _ = syncer.Run(runCtx) }()
	host, _ := os.Hostname()
	up := syncclient.NewUploader(syncclient.UploaderOptions{API: syncer.Client(), DeviceID: deviceID, DeviceName: host, Logger: log, Objects: syncclient.SharedObjectQueue(syncer.Client(), log)})
	go func() { _ = up.Run(runCtx) }()
	return syncer, up, cancel
}

func deviceID() string {
	creds, err := auth.DefaultStore(config.CredentialsFile()).Load()
	if err != nil || creds == nil {
		return ""
	}
	return creds.DeviceID
}

// loadSettings merges the settings hierarchy and returns it with the permission sources derived
// from it (CLI rules stay in Options and are applied by the engine as "cliArg").
func loadSettings(ctx context.Context, o Options, cwd string, trusted bool, docs settings.DocSource, log *slog.Logger) (*settings.Settings, []permissions.Source) {
	st, err := settings.Load(ctx, settings.Options{
		Cwd: cwd, Docs: docs, Trusted: trusted,
		CLI: settings.CLI{Model: o.Model, EffortLevel: o.Effort, OutputStyle: o.OutputStyle, PermissionMode: string(o.PermissionMode), AdditionalDirectories: o.AdditionalDirs},
	})
	if err != nil {
		log.Warn("settings could not be loaded", "err", err)
		return nil, nil
	}
	for _, p := range st.Problems {
		log.Warn("settings problem", "problem", p.Error())
	}
	in := st.PermissionInputs()
	return st, permissionSources(in)
}

// permissionSources groups settings rules by their source, highest precedence first.
func permissionSources(in settings.PermissionInputs) []permissions.Source {
	type key struct {
		level settings.Level
		path  string
		doc   string
		dir   string
	}
	idx := map[key]int{}
	var out []permissions.Source
	var levels []settings.Level
	add := func(r settings.Rule, kind byte) {
		if r.Source.Level == settings.LevelCLI {
			return
		}
		k := key{r.Source.Level, r.Source.Path, r.Source.DocID, r.Source.Dir}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, permissions.Source{
				Name:          sourceName(r.Source.Level),
				Root:          r.Source.Dir,
				RequiresTrust: r.Source.Level == settings.LevelProject,
			})
			levels = append(levels, r.Source.Level)
		}
		switch kind {
		case 'a':
			out[i].Allow = append(out[i].Allow, r.Rule)
		case 'k':
			out[i].Ask = append(out[i].Ask, r.Rule)
		case 'd':
			out[i].Deny = append(out[i].Deny, r.Rule)
		}
	}
	for _, r := range in.Deny {
		add(r, 'd')
	}
	for _, r := range in.Ask {
		add(r, 'k')
	}
	for _, r := range in.Allow {
		add(r, 'a')
	}
	// Highest precedence first (policy, local, project, user, org).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && levels[j] > levels[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
			levels[j], levels[j-1] = levels[j-1], levels[j]
		}
	}
	return out
}

func sourceName(l settings.Level) string {
	switch l {
	case settings.LevelPolicy:
		return "policySettings"
	case settings.LevelLocal:
		return "localSettings"
	case settings.LevelProject:
		return "projectSettings"
	case settings.LevelUser:
		return "userSettings"
	case settings.LevelOrg:
		return "orgSettings"
	}
	return l.String()
}

// applySettingsEnv exports the settings `env` block to the process, so Bash, hooks and stdio MCP
// servers started by this session inherit it (Claude Code semantics).
func applySettingsEnv(st *settings.Settings) {
	if st == nil {
		return
	}
	for k, v := range st.Env {
		_ = os.Setenv(k, v)
	}
}

// projectMemory builds the instruction loader for cwd.
func projectMemory(ctx context.Context, cwd string, workingDirs []string, st *settings.Settings, docs memory.DocSource, noFolder bool) *memory.Loader {
	key := memoryProjectKey(ctx, cwd, noFolder)
	var mode memory.Mode
	dir := memory.AutoMemoryDir(config.Home(), key)
	if st != nil {
		mode = st.ClaudeMdMode
		if !st.AutoMemoryEnabled {
			dir = ""
		}
	}
	return memory.New(memory.Options{
		Cwd: cwd, ProjectRoot: workspace.GitRoot(ctx, cwd), WorkingDirs: workingDirs, Mode: mode, NoAncestors: noFolder,
		Docs: docs, ProjectKey: key, AutoMemoryDir: dir,
		Approvals: &memory.FileApprovals{Path: filepath.Join(config.Home(), "import-approvals.json"), Project: key},
	})
}

// noFolderMemoryKey is the project of the sessions without a folder: each has a private directory of its own, and what
// they remember is about the user rather than a project, so they share it.
const noFolderMemoryKey = "no-folder"

func memoryProjectKey(ctx context.Context, cwd string, noFolder bool) string {
	if noFolder {
		return noFolderMemoryKey
	}
	return workspace.ProjectKey(ctx, cwd)
}

// autoMemoryStore is the auto memory of the folder the session is in now. It stays on this machine without a server.
func (s *Session) autoMemoryStore() *memory.AutoStore {
	cwd := s.Dir()
	key := memoryProjectKey(context.Background(), cwd, config.IsChatDir(cwd))
	store := &memory.AutoStore{Dir: memory.AutoMemoryDir(config.Home(), key), ProjectKey: key}
	if s.Sync != nil {
		store.Docs = syncclient.AutoMemoryDocs{API: s.Sync.Client()}
	}
	return store
}

// autoMemoryEnabled reads the user's switch again, so that it applies to a session that runs.
func (s *Session) autoMemoryEnabled() bool {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Dir(), Docs: docs, Trusted: s.IsTrusted()})
	return err != nil || st.AutoMemoryEnabled
}

// autoMemorySyncTimeout bounds what a session start does for memory in the background.
const autoMemorySyncTimeout = 15 * time.Second

// syncAutoMemory brings this machine's copy up to date without holding up the session: changes that could not be sent
// earlier go now, and the copy follows other machines.
func (s *Session) syncAutoMemory() {
	if s.Sync == nil || !s.autoMemoryEnabled() {
		return
	}
	store := s.autoMemoryStore()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), autoMemorySyncTimeout)
		defer cancel()
		_ = store.Sync(ctx)
	}()
}

// trackUpload registers the session transcript with the uploader.
func trackUpload(ctx context.Context, up *syncclient.Uploader, s *Session, path, branch string) {
	if up == nil {
		return
	}
	mode := agentv1.PermissionMode_PERMISSION_MODE_DEFAULT
	meta := syncclient.SessionMeta{
		Cwd: s.Cwd, Repo: workspace.Repo(ctx, s.Cwd), Branch: branch, Model: s.Model,
		PermissionMode: mode, Status: agentv1.SessionStatus_SESSION_STATUS_WORKING,
		ParentSessionID: s.ParentSessionID, PromptBundleVersion: s.Bundle.Version,
	}
	if w := s.Worktree; w != nil {
		meta.Worktree = &syncclient.Worktree{Path: w.Path, Branch: w.Branch, BaseRef: w.BaseRef, RepositoryPath: w.RepositoryPath}
	}
	_ = up.Track(s.ID, path, meta)
}
