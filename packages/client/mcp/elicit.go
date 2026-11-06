package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

// A form elicitation is shown through the same question UI as the agent's own questions: one question per field,
// four at a time. The answers go back to the MCP server and nowhere else; they do not enter the conversation.

// Elicitation outcomes (MCP spec).
const (
	elicitAccept  = "accept"
	elicitDecline = "decline"
	elicitCancel  = "cancel"
)

const (
	// Fits the question header limit.
	elicitHeader = "MCP form"
	// maxQuestionsAtOnce is the question UI's limit.
	maxQuestionsAtOnce = 4
	// elicitAttempts bounds re-asking after an answer that does not fit the schema.
	elicitAttempts = 3
	openLabel      = "I opened it"
	declineLabel   = "Decline"
)

// elicitResult is an elicitation's outcome.
type elicitResult struct {
	Action  string
	Content map[string]any
}

// answerElicitation asks the user. It never fails: whatever goes wrong is a decline or a cancel, which the server
// can handle.
func (m *Manager) answerElicitation(ctx context.Context, env *tools.Env, s *server, toolUseID, mode, message, schemaJSON, link string) elicitResult {
	ia := hostInteractor(env)
	if ia == nil {
		return elicitResult{Action: elicitCancel}
	}
	if mode == "url" {
		return askLink(ctx, ia, s.name(), toolUseID, message, link)
	}
	var schema elicitSchema
	if schemaJSON != "" {
		if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
			m.log.Warn("MCP elicitation with an invalid schema", "server", s.name(), "err", err)
			return elicitResult{Action: elicitCancel}
		}
	}
	fields, err := schema.fields()
	if err != nil {
		m.log.Warn("MCP elicitation with an unsupported schema", "server", s.name(), "err", err)
		return elicitResult{Action: elicitDecline}
	}
	return askForm(ctx, ia, s.name(), toolUseID, message, fields)
}

// handleDirectElicitation answers an elicitation of a direct connection. It belongs to the server's newest running
// call: without one nobody is waiting for it.
func (m *Manager) handleDirectElicitation(s *server, params *sdk.ElicitParams) (*sdk.ElicitResult, error) {
	call := m.currentCall(s)
	if call == nil {
		return &sdk.ElicitResult{Action: elicitCancel}, nil
	}
	call.timer.pause()
	defer call.timer.resume()
	raw, _ := json.Marshal(params.RequestedSchema)
	mode := params.Mode
	if mode == "" && params.URL != "" {
		mode = "url"
	}
	r := m.answerElicitation(call.ctx, call.env, s, call.id, mode, params.Message, string(raw), params.URL)
	return &sdk.ElicitResult{Action: r.Action, Content: r.Content}, nil
}

// answerRemoteElicitation answers an elicitation that came through the proxy.
func (m *Manager) answerRemoteElicitation(ctx context.Context, env *tools.Env, s *server, toolUseID string, e *mcpv1.ElicitationRequest) *mcpv1.RespondToElicitationRequest {
	r := m.answerElicitation(ctx, env, s, toolUseID, e.GetMode(), e.GetMessage(), e.GetRequestedSchemaJson(), e.GetUrl())
	out := &mcpv1.RespondToElicitationRequest{Id: e.GetId(), Action: r.Action}
	if r.Content != nil {
		b, err := json.Marshal(r.Content)
		if err == nil {
			out.ContentJson = string(b)
		}
	}
	return out
}

func askLink(ctx context.Context, ia tools.Interactor, server, toolUseID, message, link string) elicitResult {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return elicitResult{Action: elicitDecline}
	}
	noOther := false
	qs, err := tools.NormalizeQuestions([]tools.Question{{
		Header: elicitHeader, Type: tools.QuestionSingle, AllowOther: &noOther,
		Question: fmt.Sprintf("The MCP server %q asks you to open a page on %s: %s\n\n%s", server, u.Host, message, link),
		Options:  []tools.QuestionOption{{Label: openLabel, Description: "You opened the link and finished there"}, {Label: declineLabel, Description: "Do not open it"}},
	}})
	if err != nil {
		return elicitResult{Action: elicitDecline}
	}
	resp, err := ia.AskQuestions(ctx, toolUseID, qs)
	if err != nil {
		return elicitResult{Action: elicitCancel}
	}
	if resp.Outcome != tools.QuestionAnswered {
		return elicitResult{Action: elicitCancel}
	}
	if len(resp.Answers) == 1 && slices.Contains(resp.Answers[0].Selected, openLabel) {
		return elicitResult{Action: elicitAccept}
	}
	return elicitResult{Action: elicitDecline}
}

// askForm asks the fields in batches. A batch with an invalid answer is asked again with the reason.
func askForm(ctx context.Context, ia tools.Interactor, server, toolUseID, message string, fields []elicitField) elicitResult {
	content := map[string]any{}
	for start := 0; start < len(fields); start += maxQuestionsAtOnce {
		batch := fields[start:min(start+maxQuestionsAtOnce, len(fields))]
		problem := ""
		for attempt := 0; ; attempt++ {
			intro := fmt.Sprintf("The MCP server %q asks: %s", server, message)
			if problem != "" {
				intro = problem + "\n" + intro
			}
			qs := make([]tools.Question, len(batch))
			for i, f := range batch {
				qs[i] = f.question(intro)
				intro = "" // only the first question carries the server's message
			}
			norm, err := tools.NormalizeQuestions(qs)
			if err != nil {
				return elicitResult{Action: elicitDecline}
			}
			resp, err := ia.AskQuestions(ctx, toolUseID, norm)
			if err != nil || resp.Outcome != tools.QuestionAnswered || len(resp.Answers) != len(batch) {
				return elicitResult{Action: elicitCancel}
			}
			problem = ""
			for i, f := range batch {
				v, ok, err := f.parse(resp.Answers[i])
				if err != nil {
					problem = fmt.Sprintf("%s: %v.", f.label(), err)
					break
				}
				if ok {
					content[f.name] = v
				} else if f.required {
					return elicitResult{Action: elicitDecline}
				}
			}
			if problem == "" {
				break
			}
			if attempt+1 >= elicitAttempts {
				return elicitResult{Action: elicitDecline}
			}
		}
	}
	return elicitResult{Action: elicitAccept, Content: content}
}

// elicitSchema is the restricted JSON Schema of a form: a flat object of primitive properties.
type elicitSchema struct {
	Properties map[string]elicitProperty `json:"properties"`
	Required   []string                  `json:"required"`
}

type elicitProperty struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Enum        []string `json:"enum"`
	EnumNames   []string `json:"enumNames"`
	OneOf       []struct {
		Const string `json:"const"`
		Title string `json:"title"`
	} `json:"oneOf"`
	Items *struct {
		Type  string   `json:"type"`
		Enum  []string `json:"enum"`
		AnyOf []struct {
			Const string `json:"const"`
			Title string `json:"title"`
		} `json:"anyOf"`
	} `json:"items"`
	Format    string   `json:"format"`
	Pattern   string   `json:"pattern"`
	MinLength *int     `json:"minLength"`
	MaxLength *int     `json:"maxLength"`
	Minimum   *float64 `json:"minimum"`
	Maximum   *float64 `json:"maximum"`
	MinItems  *int     `json:"minItems"`
	MaxItems  *int     `json:"maxItems"`
	Default   any      `json:"default"`
}

type elicitField struct {
	name     string
	required bool
	p        elicitProperty
	// choices of an enum or multi field: label shown -> value sent.
	labels []string
	values map[string]string
}

// fields returns the form's fields: required ones first, then by name. The order of JSON object keys does not survive
// the trip through the SDK, so it cannot be used.
func (s elicitSchema) fields() ([]elicitField, error) {
	var out []elicitField
	for name, p := range s.Properties {
		f := elicitField{name: name, required: slices.Contains(s.Required, name), p: p, values: map[string]string{}}
		add := func(value, title string) {
			if title == "" {
				title = value
			}
			f.labels = append(f.labels, title)
			f.values[title] = value
		}
		switch p.Type {
		case "string":
			for i, v := range p.Enum {
				title := ""
				if i < len(p.EnumNames) {
					title = p.EnumNames[i]
				}
				add(v, title)
			}
			for _, o := range p.OneOf {
				add(o.Const, o.Title)
			}
		case "number", "integer", "boolean":
		case "array":
			if p.Items == nil {
				return nil, fmt.Errorf("field %s: an array needs items", name)
			}
			for _, v := range p.Items.Enum {
				add(v, "")
			}
			for _, o := range p.Items.AnyOf {
				add(o.Const, o.Title)
			}
			if len(f.labels) == 0 {
				return nil, fmt.Errorf("field %s: an array must list its choices", name)
			}
		default:
			return nil, fmt.Errorf("field %s: type %q is not supported", name, p.Type)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].required != out[j].required {
			return out[i].required
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

func (f elicitField) label() string {
	if f.p.Title != "" {
		return f.p.Title
	}
	return f.name
}

// question builds the field's question, with `intro` before it.
func (f elicitField) question(intro string) tools.Question {
	var text strings.Builder
	if intro != "" {
		text.WriteString(intro + "\n\n")
	}
	text.WriteString(f.label())
	if f.p.Description != "" {
		text.WriteString(": " + f.p.Description)
	}
	if !f.required {
		text.WriteString(" (optional)")
	}
	if f.p.Default != nil {
		fmt.Fprintf(&text, " Default: %v.", f.p.Default)
	}
	noOther := false
	q := tools.Question{Header: elicitHeader, Question: text.String(), AllowOther: &noOther}
	switch {
	case f.p.Type == "boolean":
		q.Type = tools.QuestionYesNo
	case f.p.Type == "array":
		q.Type = tools.QuestionMulti
		for _, l := range f.labels {
			q.Options = append(q.Options, tools.QuestionOption{Label: l})
		}
		if f.p.MinItems != nil {
			q.Min = f.p.MinItems
		}
		if f.p.MaxItems != nil {
			q.Max = f.p.MaxItems
		}
	case len(f.labels) > 0:
		q.Type = tools.QuestionSingle
		for _, l := range f.labels {
			q.Options = append(q.Options, tools.QuestionOption{Label: l})
		}
	default:
		q.Type = tools.QuestionText
	}
	return q
}

// parse validates an answer. ok false: the field was left empty.
func (f elicitField) parse(a tools.QuestionAnswer) (any, bool, error) {
	if a.Skipped {
		return nil, false, nil
	}
	text := strings.TrimSpace(a.Text)
	switch {
	case f.p.Type == "array":
		if len(a.Selected) == 0 {
			return nil, false, nil
		}
		out := make([]string, 0, len(a.Selected))
		for _, l := range a.Selected {
			v, ok := f.values[l]
			if !ok {
				return nil, false, fmt.Errorf("%q is not one of the choices", l)
			}
			out = append(out, v)
		}
		return out, true, nil
	case f.p.Type == "boolean":
		sel := ""
		if len(a.Selected) > 0 {
			sel = a.Selected[0]
		} else {
			sel = text
		}
		switch strings.ToLower(sel) {
		case "yes", "true":
			return true, true, nil
		case "no", "false":
			return false, true, nil
		case "":
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("answer yes or no")
	case len(f.labels) > 0:
		if len(a.Selected) == 0 {
			return nil, false, nil
		}
		v, ok := f.values[a.Selected[0]]
		if !ok {
			return nil, false, fmt.Errorf("%q is not one of the choices", a.Selected[0])
		}
		return v, true, nil
	}
	if text == "" {
		return nil, false, nil
	}
	switch f.p.Type {
	case "number", "integer":
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, false, fmt.Errorf("enter a number")
		}
		if f.p.Type == "integer" && n != math.Trunc(n) {
			return nil, false, fmt.Errorf("enter a whole number")
		}
		if f.p.Minimum != nil && n < *f.p.Minimum {
			return nil, false, fmt.Errorf("must be at least %v", *f.p.Minimum)
		}
		if f.p.Maximum != nil && n > *f.p.Maximum {
			return nil, false, fmt.Errorf("must be at most %v", *f.p.Maximum)
		}
		if f.p.Type == "integer" {
			return int64(n), true, nil
		}
		return n, true, nil
	}
	if f.p.MinLength != nil && utf8.RuneCountInString(text) < *f.p.MinLength {
		return nil, false, fmt.Errorf("must have at least %d characters", *f.p.MinLength)
	}
	if f.p.MaxLength != nil && utf8.RuneCountInString(text) > *f.p.MaxLength {
		return nil, false, fmt.Errorf("must have at most %d characters", *f.p.MaxLength)
	}
	if f.p.Pattern != "" {
		if re, err := regexp.Compile(f.p.Pattern); err == nil && !re.MatchString(text) {
			return nil, false, fmt.Errorf("does not have the expected form")
		}
	}
	if err := checkFormat(f.p.Format, text); err != nil {
		return nil, false, err
	}
	return text, true, nil
}

func checkFormat(format, text string) error {
	switch format {
	case "email":
		if _, err := mail.ParseAddress(text); err != nil {
			return fmt.Errorf("enter an email address")
		}
	case "uri":
		if u, err := url.Parse(text); err != nil || u.Scheme == "" {
			return fmt.Errorf("enter a full URL")
		}
	case "date":
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return fmt.Errorf("enter a date as YYYY-MM-DD")
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, text); err != nil {
			return fmt.Errorf("enter a time as RFC 3339, e.g. 2026-01-31T10:00:00Z")
		}
	}
	return nil
}
