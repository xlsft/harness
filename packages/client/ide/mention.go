package ide

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	// maxMentionBytes bounds what one mention adds to a message; maxMentions how many of them expand.
	maxMentionBytes = 64 << 10
	maxMentions     = 10
)

// mentionRe finds @path#L10 and @path#L10-20; a space in a path is written "\ ".
var mentionRe = regexp.MustCompile(`(?:^|\s)@((?:\\ |[^\s#])+)#L(\d+)(?:-L?(\d+))?`)

// Mention is one line range named in a message.
type Mention struct {
	// Raw is the path as written; Path is absolute.
	Raw, Path  string
	Start, End int
}

// ParseMentions finds the line range mentions in text, each once. A plain @path is not one: it stays text for the
// model to read with its tools.
func ParseMentions(text, cwd string) []Mention {
	var out []Mention
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		if len(out) == maxMentions {
			break
		}
		start, err := strconv.Atoi(m[2])
		if err != nil || start < 1 {
			continue
		}
		end := start
		if m[3] != "" {
			if end, err = strconv.Atoi(m[3]); err != nil || end < start {
				continue
			}
		}
		raw := strings.ReplaceAll(m[1], `\ `, " ")
		path := raw
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		path = filepath.Clean(path)
		key := fmt.Sprintf("%s#%d-%d", path, start, end)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Mention{Raw: raw, Path: path, Start: start, End: end})
	}
	return out
}

// ExpandMention returns the context block of one mention. allowed says whether the session may read a path; ed, when
// not nil, supplies a buffer the editor has open, so that unsaved text is what the model sees. "" when there is nothing
// to add: the mention stays as written and the model can look for itself.
func ExpandMention(ctx context.Context, m Mention, ed *Editor, allowed func(string) bool) string {
	if !allowed(m.Path) {
		return ""
	}
	content, ok := "", false
	if ed != nil {
		if text, open, err := ed.ReadBuffer(ctx, m.Path); err == nil && open {
			content, ok = text, true
		}
	}
	if !ok {
		st, err := os.Stat(m.Path)
		if err != nil || !st.Mode().IsRegular() {
			return ""
		}
		data, err := os.ReadFile(m.Path)
		if err != nil {
			return ""
		}
		content = string(data)
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if m.Start > len(lines) {
		return ""
	}
	end := min(m.End, len(lines))
	body := strings.Join(lines[m.Start-1:end], "\n")
	note := ""
	if len(body) > maxMentionBytes {
		cut := maxMentionBytes
		for cut > 0 && body[cut]&0xC0 == 0x80 {
			cut-- // not inside a character
		}
		body, note = body[:cut], "\n[cut: the selection is longer; read the file for the rest]"
	}
	return fmt.Sprintf("<editor-selection path=%q lines=\"%d-%d\">\n%s%s\n</editor-selection>", m.Raw, m.Start, end, body, note)
}
