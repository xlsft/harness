package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/subagent"
)

// ReadSchemaFlag reads --json-schema: inline JSON, or the path of a file holding it. Empty stays empty.
func ReadSchemaFlag(v string) (json.RawMessage, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if !strings.HasPrefix(v, "{") {
		data, err := os.ReadFile(v)
		if err != nil {
			return nil, fmt.Errorf("--json-schema: %w", err)
		}
		v = strings.TrimSpace(string(data))
	}
	if err := subagent.CheckSchema(json.RawMessage(v)); err != nil {
		return nil, fmt.Errorf("--json-schema: %w", err)
	}
	return json.RawMessage(v), nil
}

// PromptFlag reads a prompt flag and its -file form, which exclude each other.
func PromptFlag(name, text, file string) (string, error) {
	if text != "" && file != "" {
		return "", fmt.Errorf("%s and %s-file exclude each other", name, name)
	}
	if file == "" {
		return text, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("%s-file: %w", name, err)
	}
	return string(data), nil
}

// maxInputLine bounds one line of stream-json input; images come inline, so it is generous.
const maxInputLine = 32 << 20

// inputMessage is one user message of stream-json input.
type inputMessage struct {
	blocks []llm.ContentBlock
	// text is set when the content was a plain string: it may be a slash command.
	text string
	id   string
}

// stream runs stream-json input: one result per message, in the order they came. A message that arrives during a turn
// waits for the turn's end; a control interrupt stops the turn. End of input ends the run with the last turn's exit code.
func (r *runner) stream(ctx context.Context, first string) int {
	msgs := make(chan inputMessage, 64)
	scanDone := make(chan struct{})
	go func() {
		defer close(msgs)
		defer close(scanDone)
		sc := bufio.NewScanner(r.o.Stdin)
		sc.Buffer(make([]byte, 64<<10), maxInputLine)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			msg, control, err := parseInputLine(line)
			switch {
			case err != nil:
				r.inputError(err)
			case control == "interrupt":
				r.interruptTurn()
			case control != "":
				r.inputError(fmt.Errorf("unknown control subtype %q", control))
			default:
				select {
				case msgs <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := sc.Err(); err != nil {
			r.inputError(fmt.Errorf("reading input: %w", err))
		}
	}()

	code, ran := 0, false
	if first != "" {
		code, ran = r.streamTurn(ctx, inputMessage{text: first}), true
	}
	for {
		select {
		case <-ctx.Done():
			return r.finishStream(code, ran)
		case m, ok := <-msgs:
			if !ok {
				if !ran {
					return r.fatal(errors.New("no prompt given: the input ended without a user message"))
				}
				return code
			}
			code, ran = r.streamTurn(ctx, m), true
		}
	}
}

func (r *runner) finishStream(code int, ran bool) int {
	if !ran {
		return 1
	}
	return code
}

// streamTurn runs one message with a context a control interrupt can cancel.
func (r *runner) streamTurn(ctx context.Context, m inputMessage) int {
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.turnMu.Lock()
	r.cancelTurn = cancel
	r.turnMu.Unlock()
	defer func() {
		r.turnMu.Lock()
		r.cancelTurn = nil
		r.turnMu.Unlock()
	}()
	var ids []string
	if m.id != "" {
		ids = []string{m.id}
	}
	return r.turn(tctx, m.text, m.blocks, ids)
}

func (r *runner) interruptTurn() {
	r.turnMu.Lock()
	cancel := r.cancelTurn
	r.turnMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *runner) inputError(err error) {
	r.out.send(map[string]any{"type": "system", "subtype": "input_error", "error": err.Error(), "session_id": r.sessionID(), "uuid": uuid.NewString()})
}

// parseInputLine reads one line: {"type":"user","uuid":"...","message":{"role":"user","content":...}} with content a
// string or text and image blocks, or {"type":"control","subtype":"interrupt"}.
func parseInputLine(line []byte) (inputMessage, string, error) {
	var in struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		UUID    string `json:"uuid"`
		Message *struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &in); err != nil {
		return inputMessage{}, "", fmt.Errorf("not a JSON object: %w", err)
	}
	switch in.Type {
	case "control":
		if in.Subtype == "" {
			return inputMessage{}, "", errors.New("a control message needs a subtype")
		}
		return inputMessage{}, in.Subtype, nil
	case "user":
	default:
		return inputMessage{}, "", fmt.Errorf("unknown message type %q", in.Type)
	}
	if in.Message == nil || (in.Message.Role != "" && in.Message.Role != "user") {
		return inputMessage{}, "", errors.New(`a user message needs "message" with role user`)
	}
	m := inputMessage{id: in.UUID}
	var text string
	if err := json.Unmarshal(in.Message.Content, &text); err == nil {
		if text == "" {
			return inputMessage{}, "", errors.New("the message is empty")
		}
		m.text = text
		return m, "", nil
	}
	var blocks []llm.ContentBlock
	if err := json.Unmarshal(in.Message.Content, &blocks); err != nil {
		return inputMessage{}, "", fmt.Errorf("content must be a string or an array of blocks: %w", err)
	}
	if len(blocks) == 0 {
		return inputMessage{}, "", errors.New("the message is empty")
	}
	for _, b := range blocks {
		if b.Type != llm.BlockText && b.Type != llm.BlockImage {
			return inputMessage{}, "", fmt.Errorf("content block %q is not accepted in input", b.Type)
		}
	}
	m.blocks = blocks
	return m, "", nil
}
