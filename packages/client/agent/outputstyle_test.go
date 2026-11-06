package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/bootstrap"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
)

// styleSetup isolates HOME (user styles) and answers compaction requests.
func styleSetup(t *testing.T) *env {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return setup(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			return fg.Reply{Events: fg.Message("sum", fg.Text("SUMMARY"))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
}

// withCodingSections adds a doing_tasks section next to tool_policy.
func withCodingSections(b *prompts.Bundle) {
	var sys []prompts.Section
	for _, s := range b.System {
		if s.ID == "tool_policy" {
			sys = append(sys, prompts.Section{ID: "doing_tasks", Text: "DOING TASKS SECTION"})
		}
		sys = append(sys, s)
	}
	b.System = sys
}

func lastUserText(r *llm.Request) string {
	m := r.Messages[len(r.Messages)-1]
	var sb strings.Builder
	for _, b := range m.Content {
		sb.WriteString(b.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}

func systemText(r *llm.Request) string {
	var sb strings.Builder
	for _, b := range r.System {
		sb.WriteString(b.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestOutputStyleReminderAtStartAndOnSwitch(t *testing.T) {
	e := styleSetup(t)
	s := e.open(t, func(o *bootstrap.Options) { o.OutputStyle = "explanatory" })
	ctx := context.Background()
	if s.ActiveOutputStyle() != "explanatory" {
		t.Fatalf("active %q", s.ActiveOutputStyle())
	}
	s.Agent.RunTurn(ctx, prompt("hello"))
	r0 := mainRequests(e.srv)[0]
	if !strings.Contains(lastUserText(r0), "Output style: Explanatory\nExplain your choices with short insights.") {
		t.Fatalf("style reminder missing at start: %q", lastUserText(r0))
	}

	// A switch is delivered through the reminder; system prompt and tools stay the same.
	if deferred, err := s.SetOutputStyle("Learning"); err != nil || deferred {
		t.Fatalf("switch: %v %v", deferred, err)
	}
	s.Agent.RunTurn(ctx, prompt("again"))
	r1 := mainRequests(e.srv)[1]
	if !strings.Contains(lastUserText(r1), "Output style: Learning\nAsk the user to write small pieces of code.") {
		t.Fatalf("style reminder missing after switch: %q", lastUserText(r1))
	}
	if !reflect.DeepEqual(r0.System, r1.System) || !reflect.DeepEqual(r0.Tools, r1.Tools) {
		t.Fatal("a style switch must not change the system prompt or the tools")
	}

	// Setting the same style again sends nothing; switching to default says so.
	s.SetOutputStyle("learning")
	s.Agent.RunTurn(ctx, prompt("third"))
	if strings.Contains(lastUserText(mainRequests(e.srv)[2]), "Output style:") {
		t.Fatal("re-selecting the active style must not resend it")
	}
	s.SetOutputStyle("default")
	s.Agent.RunTurn(ctx, prompt("fourth"))
	r3 := mainRequests(e.srv)[3]
	if !strings.Contains(lastUserText(r3), "Output style: Default") || !reflect.DeepEqual(r0.System, r3.System) {
		t.Fatalf("switch to default: %q", lastUserText(r3))
	}
	if s.ActiveOutputStyle() != "default" {
		t.Fatalf("active %q", s.ActiveOutputStyle())
	}
	if _, err := s.SetOutputStyle("no-such-style"); err == nil {
		t.Fatal("unknown style must be an error")
	}

	// The transcript records every change (the last one wins on resume).
	var recorded []string
	for _, en := range s.Transcript.Entries() {
		if en.Type == session.TypeMeta && en.OutputStyle != "" {
			recorded = append(recorded, en.OutputStyle)
		}
	}
	if strings.Join(recorded, ",") != "explanatory,learning,default" {
		t.Fatalf("recorded styles %v", recorded)
	}
}

func TestOutputStyleWithoutCodingInstructionsAtStart(t *testing.T) {
	e := styleSetup(t)
	withCodingSections(e.srv.Bundle)
	os.MkdirAll(filepath.Join(e.proj, ".harness", "output-styles"), 0o755)
	os.WriteFile(filepath.Join(e.proj, ".harness", "output-styles", "terse.md"), []byte("---\ndescription: Short answers\n---\nAnswer in one line."), 0o644)

	s := e.open(t, func(o *bootstrap.Options) { o.OutputStyle = "terse" })
	s.Agent.RunTurn(context.Background(), prompt("hello"))
	r := mainRequests(e.srv)[0]
	sys := systemText(r)
	if strings.Contains(sys, "DOING TASKS SECTION") || strings.Contains(sys, "Use the tools to inspect and change files.") {
		t.Fatalf("coding sections must be omitted at start: %q", sys)
	}
	if !strings.Contains(sys, "You are a test agent") || !strings.Contains(sys, "Working directory:") {
		t.Fatalf("other sections must stay: %q", sys)
	}
	if !r.System[0].Cache || len(r.Tools) != len(e.srv.Bundle.Tools) {
		t.Fatalf("cache marker on the last static block and all tools: %+v", r.System)
	}
	if !strings.Contains(lastUserText(r), "Output style: terse\nAnswer in one line.") {
		t.Fatalf("reminder: %q", lastUserText(r))
	}
}

func TestOutputStyleWithoutCodingInstructionsMidSession(t *testing.T) {
	e := styleSetup(t)
	withCodingSections(e.srv.Bundle)
	os.MkdirAll(filepath.Join(e.proj, ".harness", "output-styles"), 0o755)
	os.WriteFile(filepath.Join(e.proj, ".harness", "output-styles", "terse.md"), []byte("Answer in one line."), 0o644)
	ctx := context.Background()

	s := e.open(t, nil)
	s.Agent.RunTurn(ctx, prompt("hello"))
	r0 := mainRequests(e.srv)[0]
	if strings.Contains(lastUserText(r0), "Output style:") || !strings.Contains(systemText(r0), "DOING TASKS SECTION") {
		t.Fatal("default style: no reminder, full system prompt")
	}
	deferred, err := s.SetOutputStyle("terse")
	if err != nil || !deferred {
		t.Fatalf("mid-session switch to a style without coding instructions is deferred: %v %v", deferred, err)
	}
	s.Agent.RunTurn(ctx, prompt("again"))
	r1 := mainRequests(e.srv)[1]
	if !reflect.DeepEqual(r0.System, r1.System) || !strings.Contains(lastUserText(r1), "Answer in one line.") {
		t.Fatal("the text is delivered now, the system prompt is unchanged until compaction")
	}
	if s.Agent.CodingInstructionsOmitted() {
		t.Fatal("sections omitted before compaction")
	}
	if _, err := s.Agent.Compact(ctx, ""); err != nil {
		t.Fatal(err)
	}
	s.Agent.RunTurn(ctx, prompt("after"))
	r2 := mainRequests(e.srv)[2]
	if strings.Contains(systemText(r2), "DOING TASKS SECTION") || strings.Contains(systemText(r2), "Use the tools to inspect") {
		t.Fatalf("sections must be dropped after compaction: %q", systemText(r2))
	}
	if !strings.Contains(r2.Messages[0].Content[len(r2.Messages[0].Content)-1].Text, "Answer in one line.") {
		t.Fatal("the style is re-sent after the summary replaced the history")
	}
}

func TestOutputStyleResume(t *testing.T) {
	e := styleSetup(t)
	ctx := context.Background()
	s := e.open(t, func(o *bootstrap.Options) { o.OutputStyle = "explanatory" })
	s.Agent.RunTurn(ctx, prompt("hello"))
	id := s.ID
	s.Close()

	r := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	if r.ActiveOutputStyle() != "explanatory" {
		t.Fatalf("resume must keep the style, got %q", r.ActiveOutputStyle())
	}
	r.Agent.RunTurn(ctx, prompt("again"))
	req := mainRequests(e.srv)[1]
	if strings.Contains(lastUserText(req), "Output style:") || strings.Count(reqText(req), "Output style: Explanatory") != 1 {
		t.Fatalf("the style text is already in the history and must not be resent: %q", reqText(req))
	}
	r.Close()

	// An explicit flag on resume wins and is delivered.
	r2 := e.open(t, func(o *bootstrap.Options) { o.Resume = id; o.OutputStyle = "default" })
	if r2.ActiveOutputStyle() != "default" {
		t.Fatalf("flag on resume: %q", r2.ActiveOutputStyle())
	}
	r2.Agent.RunTurn(ctx, prompt("third"))
	if !strings.Contains(lastUserText(mainRequests(e.srv)[2]), "Output style: Default") {
		t.Fatal("switching back to default on resume must tell the model")
	}
	r2.Close()
	r3 := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	if r3.ActiveOutputStyle() != "default" {
		t.Fatalf("last recorded style wins: %q", r3.ActiveOutputStyle())
	}
}

func TestOutputStyleUnknownAtStartAndSettings(t *testing.T) {
	e := styleSetup(t)
	s := e.open(t, func(o *bootstrap.Options) { o.OutputStyle = "missing" })
	if s.ActiveOutputStyle() != "default" || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], `unknown output style "missing"`) {
		t.Fatalf("unknown style: %q %v", s.ActiveOutputStyle(), s.Warnings)
	}
	s.Close()

	os.MkdirAll(filepath.Join(e.proj, ".harness"), 0o755)
	os.WriteFile(filepath.Join(e.proj, ".harness", "settings.json"), []byte(`{"outputStyle": "learning"}`), 0o644)
	trusted := true
	s2 := e.open(t, func(o *bootstrap.Options) { o.Trusted = &trusted })
	if s2.ActiveOutputStyle() != "learning" {
		t.Fatalf("settings outputStyle: %q", s2.ActiveOutputStyle())
	}
	s2.Close()
	s3 := e.open(t, func(o *bootstrap.Options) { o.Trusted = &trusted; o.OutputStyle = "explanatory" })
	if s3.ActiveOutputStyle() != "explanatory" {
		t.Fatalf("the flag wins over project settings: %q", s3.ActiveOutputStyle())
	}
}

func TestOutputStyleEmptyBundle(t *testing.T) {
	e := styleSetup(t)
	e.srv.Bundle.OutputStyles = nil
	delete(e.srv.Bundle.Reminders, "output_style")
	os.MkdirAll(filepath.Join(e.proj, ".harness", "output-styles"), 0o755)
	os.WriteFile(filepath.Join(e.proj, ".harness", "output-styles", "pirate.md"), []byte("---\nkeep-coding-instructions: true\n---\nTalk like a pirate."), 0o644)
	s := e.open(t, func(o *bootstrap.Options) { o.OutputStyle = "pirate" })
	if l := s.OutputStyles().List(); len(l) != 2 || l[0].ID != "default" || l[1].ID != "pirate" {
		t.Fatalf("styles %+v", l)
	}
	s.Agent.RunTurn(context.Background(), prompt("hello"))
	if !strings.Contains(lastUserText(mainRequests(e.srv)[0]), "Talk like a pirate.") {
		t.Fatal("without the reminder template the style text is sent as is")
	}
	if _, err := s.SetOutputStyle("explanatory"); err == nil {
		t.Fatal("no built-ins in an empty bundle")
	}
}
