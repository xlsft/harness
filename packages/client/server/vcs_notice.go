package server

import (
	"path/filepath"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/vcshooks"
)

// suggestVcsHooks points out, once per working copy on this device, that a folder under Mercurial, Jujutsu or Perforce
// gets isolation and a real diff only through hooks (docs/design/vcs-hooks.md), when the session has none of them.
func (ls *liveSession) suggestVcsHooks() {
	if isNoFolder(ls.sess.ID, ls.sess.Dir()) {
		return
	}
	if h := vcshooks.Of(ls.sess.Hooks); h.Isolates() || h.Reports() {
		return
	}
	kind, root := vcshooks.Detect(ls.sess.Dir())
	if kind == "" {
		return
	}
	n := &vcshooks.Notices{Path: filepath.Join(config.Home(), "vcs-notices.json")}
	if !n.First(root) {
		return
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_VcsHooksSuggested{VcsHooksSuggested: &agentv1.VcsHooksSuggested{Vcs: kind, Root: root}}})
}
