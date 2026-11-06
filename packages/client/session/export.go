package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
)

// Limits of a Markdown export: tool input and output are there to follow the session, and the JSONL export has them whole.
const (
	exportInputChars  = 4 << 10
	exportResultChars = 8 << 10
)

// ExportMeta is what heads a Markdown export.
type ExportMeta struct {
	ID    string
	Title string
	Cwd   string
}

// RenderMarkdown turns a transcript into a readable document: the active branch's messages, tool calls and results,
// without the entries that are only for the model (reminders, instructions, summaries of compactions).
func RenderMarkdown(entries []Entry, meta ExportMeta) []byte {
	var b strings.Builder
	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = ReadTitles(entries).Best()
	}
	if title == "" {
		title = "Session " + meta.ID
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- Session: `%s`\n", meta.ID)
	cwd := meta.Cwd
	first, last := "", ""
	for _, e := range entries {
		if cwd == "" {
			cwd = e.Cwd
		}
		if e.Timestamp != "" {
			if first == "" {
				first = e.Timestamp
			}
			last = e.Timestamp
		}
	}
	if cwd != "" {
		fmt.Fprintf(&b, "- Folder: `%s`\n", cwd)
	}
	if first != "" {
		fmt.Fprintf(&b, "- From %s to %s\n", first, last)
	}
	b.WriteString("\n")
	for _, e := range ActiveChain(entries, true) {
		if e.IsMeta || e.IsSidechain || e.Message == nil || e.IsCompactSummary {
			continue
		}
		switch e.Type {
		case TypeUser:
			renderUser(&b, &e)
		case TypeAssistant:
			renderAssistant(&b, &e)
		}
	}
	return []byte(b.String())
}

func renderUser(b *strings.Builder, e *Entry) {
	var text []string
	var results []llm.ContentBlock
	for _, c := range e.Blocks() {
		switch c.Type {
		case llm.BlockText:
			if strings.TrimSpace(c.Text) != "" {
				text = append(text, c.Text)
			}
		case llm.BlockImage, llm.BlockDocument:
			text = append(text, "*[attachment]*")
		case llm.BlockToolResult:
			results = append(results, c)
		}
	}
	if len(text) > 0 {
		fmt.Fprintf(b, "## User\n\n%s\n\n", strings.Join(text, "\n\n"))
	}
	for _, r := range results {
		label := "Result"
		if r.IsError {
			label = "Error"
		}
		fmt.Fprintf(b, "%s:\n\n%s\n", label, fence(clip(toolResultText(r), exportResultChars), ""))
	}
}

func renderAssistant(b *strings.Builder, e *Entry) {
	headed := false
	for _, c := range e.Blocks() {
		switch c.Type {
		case llm.BlockText:
			if strings.TrimSpace(c.Text) == "" {
				continue
			}
			if !headed {
				b.WriteString("## Assistant\n\n")
				headed = true
			}
			b.WriteString(c.Text + "\n\n")
		case llm.BlockToolUse:
			fmt.Fprintf(b, "### Tool: %s\n\n%s\n", c.Name, fence(clip(toolInputText(c.Input), exportInputChars), "json"))
		}
	}
}

func toolInputText(input json.RawMessage) string {
	var v any
	if json.Unmarshal(input, &v) == nil {
		if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(pretty)
		}
	}
	return string(input)
}

// toolResultText reads a result block's content.
func toolResultText(c llm.ContentBlock) string {
	var out []string
	for _, p := range c.Content {
		if p.Type == llm.BlockText {
			out = append(out, p.Text)
		} else {
			out = append(out, "["+p.Type+"]")
		}
	}
	return strings.Join(out, "\n")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + fmt.Sprintf("\n… [%d more bytes]", len(s)-n)
}

// fence wraps text in a code fence longer than any it contains, so nothing in it can end the block.
func fence(text, lang string) string {
	longest, run := 2, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	marks := strings.Repeat("`", longest+1)
	return marks + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + marks + "\n"
}

// EntryText is the text a person would search an entry for: what was said and what tools answered. Empty for entries
// only the model sees.
func EntryText(e *Entry) string {
	if e.IsMeta || e.IsSidechain || e.Message == nil || (e.Type != TypeUser && e.Type != TypeAssistant) {
		return ""
	}
	var parts []string
	for _, c := range e.Blocks() {
		switch c.Type {
		case llm.BlockText:
			parts = append(parts, c.Text)
		case llm.BlockToolResult:
			parts = append(parts, toolResultText(c))
		}
	}
	return strings.Join(parts, "\n")
}
