package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Visual limits.
const (
	maxVisualHTML  = 512 << 10
	maxVisualTitle = 80
)

var visualIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// HTMLRenderer is implemented by an Interactor whose frontends can render
// HTML visuals in the transcript (AgentService with a graphical UI).
type HTMLRenderer interface {
	RendersHTML() bool
}

// Visual shows a self-contained HTML document (a chart, a diagram, a small
// interactive widget) in the transcript. The frontend renders it from the
// tool input in a sandboxed frame; nothing runs on this machine, so it needs
// no permission.
type Visual struct{}

func (*Visual) Name() string                  { return "Visual" }
func (*Visual) ReadOnly(json.RawMessage) bool { return true }

type visualInput struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	HTML  string `json:"html"`
}

func parseVisual(input json.RawMessage) (visualInput, error) {
	var in visualInput
	if err := decode(input, &in); err != nil {
		return in, err
	}
	bad := func(format string, args ...any) (visualInput, error) {
		return in, fmt.Errorf("InputValidationError: "+format, args...)
	}
	in.Title = strings.TrimSpace(in.Title)
	switch {
	case !visualIDPattern.MatchString(in.ID):
		return bad("id must be 1-48 lowercase letters, digits or dashes, starting with a letter or digit")
	case in.Title == "":
		return bad("title is required")
	case len([]rune(in.Title)) > maxVisualTitle:
		return bad("title is longer than %d characters", maxVisualTitle)
	case strings.TrimSpace(in.HTML) == "":
		return bad("html is required")
	case len(in.HTML) > maxVisualHTML:
		return bad("html is %d KB; the limit is %d KB", len(in.HTML)>>10, maxVisualHTML>>10)
	}
	return in, nil
}

func (*Visual) Run(ctx context.Context, env *Env, call Call) Result {
	in, err := parseVisual(call.Input)
	if err != nil {
		return ErrorResult("%v", err)
	}
	renders := false
	if h := env.Host(); h != nil {
		if r, ok := h.Interactor().(HTMLRenderer); ok {
			renders = r.RendersHTML()
		}
	}
	structured := map[string]any{"id": in.ID, "title": in.Title, "shown": renders}
	if !renders {
		r := TextResult("Nothing was shown: no interface that renders visuals is attached to this session. Describe the result in your reply instead.", structured)
		r.Summary = "Not shown (no UI)"
		return r
	}
	r := TextResult(fmt.Sprintf("Showed the visual %q (id %s) in the conversation. Messages that start with [from visual %s] were sent by the user from inside it.", in.Title, in.ID, in.ID), structured)
	r.Summary = "Showed " + in.Title
	return r
}
