package tui

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"

	"github.com/xlsft/harness/packages/cli/internal/client"
	"github.com/xlsft/harness/packages/cli/internal/render"
)

// optionList is the shared state of a numbered option prompt with an optional comment field.
type optionList struct {
	sel        int
	n          int
	commenting bool
	comment    editor
}

// key handles navigation; it returns the chosen index (or -1), and whether Esc was pressed.
func (o *optionList) key(k tea.KeyPressMsg, allowComment bool) (chosen int, esc bool) {
	if o.commenting {
		switch k.String() {
		case "enter":
			return o.sel, false
		case "esc":
			o.commenting = false
			o.comment.Reset()
			return -1, false
		case "tab":
			o.commenting = false
			return -1, false
		}
		editKey(&o.comment, k)
		return -1, false
	}
	switch k.String() {
	case "up", "ctrl+p", "k":
		o.sel = (o.sel + o.n - 1) % o.n
	case "down", "ctrl+n", "j":
		o.sel = (o.sel + 1) % o.n
	case "enter":
		return o.sel, false
	case "esc":
		return -1, true
	case "tab":
		if allowComment {
			o.commenting = true
		}
	default:
		if t := k.Text; len(t) == 1 && t[0] >= '1' && t[0] <= '9' {
			if i := int(t[0] - '1'); i < o.n {
				o.sel = i
				return i, false
			}
		}
	}
	return -1, false
}

func (o *optionList) apply(p *render.Prompt) {
	p.Selected = o.sel
	p.Commenting = o.commenting
	p.Comment = o.comment.text
	p.CommentAt = o.comment.cursor
}

// ---- permission ----

type permPrompt struct {
	req  *agentv1.PermissionRequest
	call *render.ToolCall
	diff *render.FileDiff
	opts optionList
}

func isEditTool(name string) bool {
	return name == "Edit" || name == "Write" || name == "MultiEdit" || name == "NotebookEdit" || name == "DocumentEdit"
}

func (m *Model) addPermission(r *agentv1.PermissionRequest) {
	c := m.calls[r.GetToolUseId()]
	if c == nil {
		c = m.newCall(r.GetToolUseId(), r.GetToolName(), r.GetInputJson())
		m.addCall(c)
	}
	p := &permPrompt{req: r, call: c, opts: optionList{n: 3}}
	if r.GetOneTimeOnly() {
		p.opts.n = 2
	}
	if isEditTool(r.GetToolName()) {
		p.diff = previewDiff(c, r.GetInputJson())
		c.Diff = p.diff
	}
	m.perms = append(m.perms, p)
}

func (m *Model) dropPermission(id string) {
	for i, p := range m.perms {
		if p.req.GetRequestId() == id {
			m.perms = append(m.perms[:i], m.perms[i+1:]...)
			return
		}
	}
}

func (p *permPrompt) rule() string {
	if rs := p.req.GetSuggestedRules(); len(rs) > 0 {
		return rs[0]
	}
	if a := p.req.GetAgentName(); a != "" {
		return p.req.GetToolName() + " (agent: " + a + ")"
	}
	return p.req.GetToolName()
}

// view builds the permission box (reference §5, rendered as the brand's rounded box).
func (m *Model) permView(p *permPrompt, width int) render.Prompt {
	s := m.st
	c := p.call
	var pr render.Prompt
	name := c.Path
	switch {
	case c.Name == "Bash":
		pr.Title = "Bash command"
		pr.Body = s.CommandPreview(c.Str("command"), c.Str("description"), width)
		pr.Question = "Do you want to proceed?"
	case isEditTool(c.Name):
		pr.Title, pr.Subtitle, pr.Dashed = "Edit file", name, true
		pr.Question = "Do you want to make this edit to " + s.Bold.Render(name) + "?"
		if p.diff != nil && p.diff.Created {
			pr.Title = "Create file"
			pr.Question = "Do you want to create " + s.Bold.Render(name) + "?"
			pr.Body = s.WritePreview(p.diff, "", width-4, 12)
		} else if p.diff != nil {
			pr.Body = s.DiffBody(p.diff, "", width-4)
			if len(pr.Body) > 16 {
				pr.Body, pr.More = pr.Body[:16], true
			}
		}
	case c.Path != "":
		pr.Title = map[string]string{"Read": "Read file", "NotebookRead": "Read notebook"}[c.Name]
		if pr.Title == "" {
			pr.Title = render.DisplayName(c.Name)
		}
		pr.Subtitle = c.AbsPath
		pr.Question = "Do you want to allow " + s.Bold.Render(render.DisplayName(c.Name)) + " on " + s.Bold.Render(name) + "?"
	default:
		pr.Title = "Tool use"
		pr.Subtitle = render.DisplayName(c.Name)
		in := p.req.GetInputJson()
		var pretty map[string]any
		if json.Unmarshal([]byte(in), &pretty) == nil {
			for k, v := range pretty {
				b, _ := json.Marshal(v)
				pr.Body = append(pr.Body, "  "+s.Sec.Render(k+": ")+s.Truncate(string(b), width-10-len(k)))
			}
		}
		pr.Question = "Do you want to allow " + s.Bold.Render(render.DisplayName(c.Name)) + "?"
	}
	if r := p.req.GetReason(); r != "" {
		pr.Question = s.Sec.Render(r) + "\n" + pr.Question
	}
	second := render.Option{Label: "Yes, and don’t ask again for " + p.rule()}
	if isEditTool(c.Name) {
		second = render.Option{Label: "Yes, allow all edits during this session", Key: "(shift+tab)"}
	}
	pr.Options = []render.Option{
		{Label: "Yes", Placeholder: "and tell harness what to do next"},
		second,
		{Label: "No, and tell harness what to do differently", Key: "(esc)", Placeholder: "tell harness what to do differently"},
	}
	pr.Options[1].Placeholder = "and tell harness what to do next"
	if p.req.GetOneTimeOnly() {
		pr.Options = []render.Option{pr.Options[0], pr.Options[2]}
	}
	if rc := p.req.GetRiskCategories(); len(rc) > 0 {
		pr.Question = s.Warning.Render("Risk: "+strings.Join(rc, ", ")) + "\n" + pr.Question
	}
	pr.Hint = "Esc to cancel · Tab to add a comment"
	if p.opts.commenting {
		pr.Hint = "Enter to confirm · Esc to discard the comment"
	}
	p.opts.apply(&pr)
	return pr
}

func (m *Model) permKey(p *permPrompt, k tea.KeyPressMsg) tea.Cmd {
	if k.String() == "shift+tab" && isEditTool(p.call.Name) && !p.opts.commenting && !p.req.GetOneTimeOnly() {
		return m.answerPerm(p, 1)
	}
	chosen, esc := p.opts.key(k, true)
	if esc {
		return m.answerPerm(p, 2)
	}
	if chosen >= 0 {
		return m.answerPerm(p, chosen)
	}
	return nil
}

func (m *Model) answerPerm(p *permPrompt, choice int) tea.Cmd {
	if p.req.GetOneTimeOnly() && choice == 1 {
		choice = 2 // the list is Yes / No
	}
	m.dropPermission(p.req.GetRequestId())
	comment := strings.TrimSpace(p.opts.comment.String())
	id := p.req.GetRequestId()
	ctx := m.ctx
	var cmds []tea.Cmd
	decision := agentv1.RespondToPermissionRequest_DECISION_ALLOW
	rule := ""
	switch choice {
	case 1:
		if isEditTool(p.call.Name) {
			m.mode = agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS
			cmds = append(cmds, func() tea.Msg {
				if err := m.conn.SetPermissionMode(ctx, agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS); err != nil {
					return errMsg{err}
				}
				return nil
			})
		} else {
			decision, rule = agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS, p.rule()
		}
	case 2:
		decision = agentv1.RespondToPermissionRequest_DECISION_DENY
		m.rejected[p.call.ID] = true
	}
	m.status = "Thinking"
	cmds = append(cmds, func() tea.Msg {
		if err := m.conn.RespondToPermission(ctx, id, decision, rule, comment); err != nil {
			return errMsg{err}
		}
		return nil
	})
	return tea.Sequence(cmds...)
}

// ---- AskUserQuestion ----

type questionPrompt struct {
	req     *agentv1.Question
	idx     int // current question
	answers []client.Answer
	checked []bool
	opts    optionList
	typing  bool
	free    editor
}

func newQuestionPrompt(q *agentv1.Question) *questionPrompt {
	p := &questionPrompt{req: q}
	p.reset()
	return p
}

func (p *questionPrompt) item() *agentv1.QuestionItem { return p.req.GetItems()[p.idx] }

func (p *questionPrompt) reset() {
	n := len(p.item().GetOptions())
	p.opts = optionList{n: n + 1}
	p.checked = make([]bool, n)
	p.typing = false
	p.free.Reset()
}

func (m *Model) questionView(p *questionPrompt, width int) render.Prompt {
	it := p.item()
	var pr render.Prompt
	for _, q := range p.req.GetItems() {
		pr.Chips = append(pr.Chips, q.GetHeader())
	}
	pr.Title = it.GetQuestion()
	for i, o := range it.GetOptions() {
		opt := render.Option{Label: o.GetLabel(), Desc: o.GetDescription()}
		if it.GetMultiSelect() {
			opt.Checked = &p.checked[i]
		}
		pr.Options = append(pr.Options, opt)
	}
	pr.Options = append(pr.Options, render.Option{Label: "Type something", Placeholder: "type your answer"})
	pr.Hint = "Enter to select · ↑/↓ to navigate · Esc to cancel"
	if it.GetMultiSelect() {
		pr.Hint = "Space to toggle · Enter to confirm · Esc to cancel"
	}
	p.opts.apply(&pr)
	if p.typing {
		pr.Commenting, pr.Comment, pr.CommentAt = true, p.free.text, p.free.cursor
	}
	return pr
}

func (m *Model) questionKey(p *questionPrompt, k tea.KeyPressMsg) tea.Cmd {
	it := p.item()
	free := len(it.GetOptions())
	if p.typing {
		switch k.String() {
		case "enter":
			return m.answerQuestion(p, client.Answer{Text: strings.TrimSpace(p.free.String())})
		case "esc":
			p.typing = false
			return nil
		}
		editKey(&p.free, k)
		return nil
	}
	if it.GetMultiSelect() && k.String() == "space" && p.opts.sel < free {
		p.checked[p.opts.sel] = !p.checked[p.opts.sel]
		return nil
	}
	chosen, esc := p.opts.key(k, false)
	if esc {
		id := p.req.GetRequestId()
		m.question = nil
		ctx := m.ctx
		return func() tea.Msg { _ = m.conn.Interrupt(ctx); _ = id; return nil }
	}
	if chosen < 0 {
		return nil
	}
	if chosen == free {
		p.typing = true
		return nil
	}
	if it.GetMultiSelect() {
		var sel []string
		for i, c := range p.checked {
			if c {
				sel = append(sel, it.GetOptions()[i].GetLabel())
			}
		}
		if len(sel) == 0 {
			sel = []string{it.GetOptions()[chosen].GetLabel()}
		}
		return m.answerQuestion(p, client.Answer{Selected: sel})
	}
	return m.answerQuestion(p, client.Answer{Selected: []string{it.GetOptions()[chosen].GetLabel()}})
}

func (m *Model) answerQuestion(p *questionPrompt, a client.Answer) tea.Cmd {
	p.answers = append(p.answers, a)
	if p.idx+1 < len(p.req.GetItems()) {
		p.idx++
		p.reset()
		return nil
	}
	m.question = nil
	// Record the answers in the transcript like the reference (§8b).
	var lines []string
	for i, q := range p.req.GetItems() {
		ans := append(append([]string(nil), p.answers[i].Selected...), p.answers[i].Text)
		lines = append(lines, "· "+q.GetQuestion()+" → "+strings.Trim(strings.Join(ans, ", "), ", "))
	}
	m.closeOpen()
	m.push(&item{kind: itemNotice, text: "You answered:\n" + strings.Join(lines, "\n")})
	id, answers, ctx := p.req.GetRequestId(), p.answers, m.ctx
	return tea.Batch(m.flush(), func() tea.Msg {
		if err := m.conn.AnswerQuestion(ctx, id, answers); err != nil {
			return errMsg{err}
		}
		return nil
	})
}

// ---- plan approval ----

type planPrompt struct {
	req    *agentv1.PlanProposed
	opts   optionList
	scroll int
}

func newPlanPrompt(p *agentv1.PlanProposed) *planPrompt {
	return &planPrompt{req: p, opts: optionList{n: 3}}
}

func (m *Model) planView(p *planPrompt, width int) render.Prompt {
	s := m.st
	body := strings.Split(s.Markdown(p.req.GetPlan(), width-6), "\n")
	maxRows := max(m.height-18, 6)
	p.scroll = min(p.scroll, max(len(body)-maxRows, 0))
	more := false
	if len(body) > maxRows {
		body = body[p.scroll:min(len(body), p.scroll+maxRows)]
		more = p.scroll+maxRows < len(strings.Split(s.Markdown(p.req.GetPlan(), width-6), "\n"))
	}
	pr := render.Prompt{
		Title: "Ready to code?", Subtitle: "Here is harness's plan:", Body: body, Dashed: true, More: more,
		Question: "harness has written up a plan and is ready to execute. Would you like to proceed?",
		Options: []render.Option{
			{Label: "Yes, and auto-accept edits"},
			{Label: "Yes, manually approve edits"},
			{Label: "No, keep planning", Placeholder: "tell harness what to change"},
		},
		Hint: "Enter to select · Tab to add feedback · pgup/pgdn to scroll · " + m.rel(p.req.GetPlanFilePath()),
	}
	p.opts.apply(&pr)
	return pr
}

func (m *Model) planKey(p *planPrompt, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "pgdown", "ctrl+d":
		p.scroll += 5
		return nil
	case "pgup", "ctrl+u":
		p.scroll = max(p.scroll-5, 0)
		return nil
	}
	chosen, esc := p.opts.key(k, true)
	if esc {
		chosen = 2
	}
	if chosen < 0 {
		return nil
	}
	d := []agentv1.RespondToPlanRequest_Decision{
		agentv1.RespondToPlanRequest_DECISION_APPROVE_ACCEPT_EDITS,
		agentv1.RespondToPlanRequest_DECISION_APPROVE_MANUAL,
		agentv1.RespondToPlanRequest_DECISION_KEEP_PLANNING,
	}[chosen]
	feedback := strings.TrimSpace(p.opts.comment.String())
	id, ctx := p.req.GetRequestId(), m.ctx
	m.plan = nil
	label := []string{"approved the plan (auto-accept edits)", "approved the plan (manual approval)", "asked to keep planning"}[chosen]
	m.closeOpen()
	m.push(&item{kind: itemNotice, text: "You " + label})
	return tea.Batch(m.flush(), func() tea.Msg {
		if err := m.conn.RespondToPlan(ctx, id, d, feedback); err != nil {
			return errMsg{err}
		}
		return nil
	})
}

// ---- generic choice (model picker, rewind) ----

type choicePrompt struct {
	title, subtitle, question, hint string
	options                         []render.Option
	opts                            optionList
	pick                            func(i int) tea.Cmd
}

func (m *Model) choiceView(p *choicePrompt) render.Prompt {
	pr := render.Prompt{Title: p.title, Subtitle: p.subtitle, Question: p.question, Options: p.options, Hint: p.hint}
	p.opts.apply(&pr)
	return pr
}

func (m *Model) choiceKey(p *choicePrompt, k tea.KeyPressMsg) tea.Cmd {
	chosen, esc := p.opts.key(k, false)
	if esc {
		m.choice = nil
		return nil
	}
	if chosen < 0 {
		return nil
	}
	m.choice = nil
	return p.pick(chosen)
}
