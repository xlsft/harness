package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

// RewindMode selects what a rewind restores.
type RewindMode int

// Rewind modes.
const (
	RewindCodeAndConversation RewindMode = iota + 1
	RewindConversation
	RewindCode
	// RewindSummarizeFrom compacts everything from the checkpoint on.
	RewindSummarizeFrom
	// RewindSummarizeUpTo compacts everything before the checkpoint.
	RewindSummarizeUpTo
)

// RewindResult reports a rewind.
type RewindResult struct {
	CheckpointID  string
	Mode          RewindMode
	FilesRestored int
	FilesSkipped  int
	// SkippedReasons maps skipped paths to why they were skipped.
	SkippedReasons map[string]string
	// RestoredPrompt goes back into the input box (conversation restores and
	// summarize-from).
	RestoredPrompt string
}

// ErrNoTranscript is returned when rewinding without a transcript.
var ErrNoTranscript = errors.New("rewind needs the session transcript")

// Checkpoints lists the rewind points of the active conversation, oldest
// first (checkpoints of rewound-away branches are hidden).
func (a *Agent) Checkpoints() []Checkpoint {
	fh, w := a.cfg.FileHistory, a.cfg.Transcript
	if fh == nil || w == nil {
		return nil
	}
	entries := w.Entries()
	conv := session.Rebuild(entries)
	onChain := map[string]bool{}
	for _, id := range conv.Chain {
		onChain[id] = true
	}
	byUUID := map[string]session.Entry{}
	for _, e := range entries {
		if e.UUID != "" {
			byUUID[e.UUID] = e
		}
	}
	var out []Checkpoint
	for _, s := range fh.Checkpoints() {
		if !onChain[s.MessageID] {
			continue
		}
		e := byUUID[s.MessageID]
		created, _ := time.Parse("2006-01-02T15:04:05.000Z", s.Timestamp)
		out = append(out, Checkpoint{ID: s.MessageID, UserMessageUUID: s.MessageID, PromptPreview: preview(e.Blocks()),
			CreatedAt: created, HasCodeChanges: fh.HasChanges(s.MessageID)})
	}
	return out
}

func promptText(e session.Entry) string {
	var parts []string
	for _, b := range e.Blocks() {
		if b.Type == llm.BlockText {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// reload rebuilds the in-memory conversation from the transcript.
func (a *Agent) reload() session.Conversation {
	conv := session.Rebuild(a.cfg.Transcript.Entries())
	a.mu.Lock()
	a.messages = append([]llm.Message(nil), conv.Messages...)
	a.contextInjected = conv.ContextInjected
	a.lastUsage = 0
	a.afterReloadStyle()
	a.mu.Unlock()
	return conv
}

// Rewind restores code and/or conversation to the checkpoint taken before
// the turn started by user message checkpointID, or summarizes from / up to
// it. The session must be idle.
func (a *Agent) Rewind(ctx context.Context, checkpointID string, mode RewindMode, instructions string) (RewindResult, error) {
	res := RewindResult{CheckpointID: checkpointID, Mode: mode}
	if a.Busy() {
		return res, ErrBusy
	}
	w := a.cfg.Transcript
	if w == nil {
		return res, ErrNoTranscript
	}
	entries := w.Entries()
	var target session.Entry
	found := false
	for _, e := range entries {
		if e.UUID == checkpointID && e.Type == session.TypeUser {
			target, found = e, true
		}
	}
	if !found {
		return res, fmt.Errorf("unknown checkpoint %s", checkpointID)
	}

	restoreCode := func() error {
		if a.cfg.FileHistory == nil {
			return errors.New("file checkpoints are not available in this session")
		}
		r, err := a.cfg.FileHistory.Restore(checkpointID)
		if err != nil {
			return err
		}
		res.FilesRestored, res.FilesSkipped, res.SkippedReasons = len(r.Restored), len(r.Skipped), r.Reasons
		if env := a.cfg.ToolEnv; env != nil {
			for _, p := range r.Restored {
				env.Files.Forget(p) // must be read again before editing
			}
		}
		return nil
	}
	rewindConversation := func() {
		parent, _ := session.TurnParent(entries, checkpointID)
		_, _ = w.AppendBranch(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeRewind, Level: "info",
			Content: "Conversation rewound to checkpoint " + checkpointID}, parent)
		conv := a.reload()
		var todos []tools.Todo
		if len(conv.Todos) > 0 {
			todos, _ = tools.ParseTodos([]byte(`{"todos":` + string(conv.Todos) + `}`))
		}
		a.SetTodos(todos)
	}

	switch mode {
	case RewindCode:
		if err := restoreCode(); err != nil {
			return res, err
		}
	case RewindCodeAndConversation:
		if err := restoreCode(); err != nil {
			return res, err
		}
		rewindConversation()
		res.RestoredPrompt = promptText(target)
	case RewindConversation:
		rewindConversation()
		res.RestoredPrompt = promptText(target)
	case RewindSummarizeFrom, RewindSummarizeUpTo:
		if err := a.summarizeAround(ctx, entries, checkpointID, mode, instructions); err != nil {
			return res, err
		}
		if mode == RewindSummarizeFrom {
			res.RestoredPrompt = promptText(target)
		}
	default:
		return res, fmt.Errorf("unknown rewind mode %d", mode)
	}
	r := res
	a.emit(Event{Kind: EventRewound, Rewind: &r})
	return res, nil
}

func messagesOf(entries []session.Entry) []llm.Message {
	var out []llm.Message
	for _, e := range entries {
		if (e.Type != session.TypeUser && e.Type != session.TypeAssistant) || e.Message == nil {
			continue
		}
		blocks := e.Blocks()
		if len(blocks) == 0 {
			continue
		}
		role := e.Message.Role
		if role == "" {
			role = e.Type
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			continue
		}
		out = append(out, llm.Message{Role: role, Content: blocks})
	}
	return out
}

// summarizeAround implements the two summarize rewind modes as targeted
// compactions; files are untouched and the original entries stay in the
// transcript.
func (a *Agent) summarizeAround(ctx context.Context, entries []session.Entry, checkpointID string, mode RewindMode, instructions string) error {
	w := a.cfg.Transcript
	chain := session.ActiveChain(entries, false)
	parent, _ := session.TurnParent(entries, checkpointID)
	// The suffix starts right after the turn's parent (so it includes the
	// turn's project-context meta entry).
	split, onChain := -1, false
	if parent == nil {
		split = 0
	}
	for i, e := range chain {
		if parent != nil && e.UUID == *parent {
			split = i + 1
		}
		if e.UUID == checkpointID {
			onChain = split >= 0
			break
		}
	}
	if !onChain {
		return errors.New("the checkpoint is before the last compaction; restore the conversation instead")
	}
	prefix, suffix := chain[:split], chain[split:]
	before := a.EstimateTokens()
	switch mode {
	case RewindSummarizeFrom:
		summary, err := a.summarize(ctx, messagesOf(suffix), instructions)
		if err != nil {
			return err
		}
		_, _ = w.AppendBranch(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeRewind, Level: "info",
			Content: "Summarized conversation from checkpoint " + checkpointID, CompactMetadata: &session.CompactMetadata{Trigger: "rewind", PreTokens: before}}, parent)
		_, _ = w.Append(session.Entry{Type: session.TypeUser, Message: session.UserBlocks(a.summaryBlocks(summary)), IsCompactSummary: true, IsVisibleInTranscriptOnly: true})
	case RewindSummarizeUpTo:
		pm := messagesOf(prefix)
		if len(pm) == 0 {
			return errors.New("there is nothing before this checkpoint to summarize")
		}
		summary, err := a.summarize(ctx, pm, instructions)
		if err != nil {
			return err
		}
		_, _ = w.AppendBranch(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeCompactBoundary, Level: "info", Content: "Summarized conversation up to checkpoint " + checkpointID,
			LogicalParentUUID: w.LastUUID(), CompactMetadata: &session.CompactMetadata{Trigger: "rewind", PreTokens: before}}, nil)
		if a.cfg.ProjectContext != "" {
			block := a.projectContextBlock()
			_, _ = w.Append(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{block}), IsMeta: true})
		}
		_, _ = w.Append(session.Entry{Type: session.TypeUser, Message: session.UserBlocks(a.summaryBlocks(summary)), IsCompactSummary: true, IsVisibleInTranscriptOnly: true})
		// Re-chain the kept turns after the summary, keeping their uuids so
		// checkpoints stay valid.
		for _, e := range suffix {
			if e.Type == session.TypeSystem && e.Subtype == session.SubtypeCompactBoundary {
				continue
			}
			e.ParentUUID = nil
			_, _ = w.Append(e)
		}
	}
	a.reload()
	info := CompactInfo{Trigger: "rewind", TokensBefore: before, TokensAfter: a.EstimateTokens()}
	a.emit(Event{Kind: EventCompacted, Compaction: &info})
	return nil
}
