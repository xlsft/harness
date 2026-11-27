package sources

import (
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/memory"
)

// Building blocks the readers share. A reader file should mostly be paths and a few calls to these.

// exists says whether p exists.
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// anyExists says whether one of the paths exists.
func anyExists(paths ...string) bool {
	for _, p := range paths {
		if exists(p) {
			return true
		}
	}
	return false
}

// stripJSONC removes // and /* */ comments and trailing commas (VS Code's JSON with comments), leaving strings alone.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inStr, esc := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case inStr:
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++
		case c == ',':
			j := i + 1
			for j < len(data) && strings.ContainsRune(" \t\r\n", rune(data[j])) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue // trailing comma
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// mcpFile turns a JSON file of MCP servers under key into items. source is the file for display.
func mcpFile(data []byte, key, source string, jsonc bool) []importer.Found {
	if jsonc {
		data = stripJSONC(data)
	}
	servers, err := importer.ParseMCPServers(data, key)
	if err != nil {
		return []importer.Found{{Kind: importer.KindMCP, Name: path.Base(source), Source: source, Status: importer.StatusError, Note: "invalid JSON: " + err.Error()}}
	}
	var out []importer.Found
	for _, name := range sortedNames(servers) {
		f := importer.Found{Kind: importer.KindMCP, Name: name, Source: source, MCP: servers[name]}
		if f.MCP == nil {
			f.Status, f.Note = importer.StatusError, "not a server entry"
		}
		out = append(out, f)
	}
	return out
}

func sortedNames[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

// instructions reads a whole file as always-on instructions; its frontmatter, if any, is dropped.
func instructions(f importer.FolderFile) []importer.Found {
	_, body, ok := memory.SplitFrontmatter(string(f.Data))
	if !ok {
		body = string(f.Data)
	}
	return []importer.Found{{Kind: importer.KindInstructions, Name: path.Base(f.Rel), Text: strings.TrimSpace(body)}}
}

// ruleName is a rule file's name below its rules folder, without the extensions: ".cursor/rules/a/b.mdc" with
// prefix ".cursor/rules/" → "a/b".
func ruleName(rel, prefix string, exts ...string) string {
	name := rel
	if i := strings.Index(rel, prefix); i >= 0 {
		name = rel[i+len(prefix):]
	}
	for _, e := range exts {
		if strings.HasSuffix(strings.ToLower(name), e) {
			return name[:len(name)-len(e)]
		}
	}
	return strings.TrimSuffix(name, path.Ext(name))
}

// harnessRule builds a harness rule file: `paths:` frontmatter when globs limit it, then the body.
func harnessRule(globs []string, body string) string {
	body = strings.TrimSpace(body)
	if len(globs) == 0 {
		return body + "\n"
	}
	var b strings.Builder
	b.WriteString("---\npaths:\n")
	for _, g := range globs {
		fmt.Fprintf(&b, "  - %q\n", g)
	}
	b.WriteString("---\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

// globList reads a frontmatter glob field that may be a list or a comma-separated string.
func globList(markdown, key string) []string {
	var out []string
	for _, g := range memory.FrontmatterList(markdown, key) {
		for _, p := range strings.Split(g, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// body is markdown without its frontmatter.
func body(markdown string) string {
	if _, b, ok := memory.SplitFrontmatter(markdown); ok {
		return b
	}
	return markdown
}

// fm reads a scalar frontmatter field.
func fm(markdown, key string) string { return memory.FrontmatterString(markdown, key) }

// rule is a rule item from a tool's rules folder: globs limit it, onDemand says the tool loaded it only when asked or
// when it judged it relevant, which harness has no equivalent for (it then always applies, and the note says so).
func rule(name, markdown string, globs []string, onDemand string) importer.Found {
	f := importer.Found{Kind: importer.KindRule, Name: name, Text: harnessRule(globs, body(markdown))}
	if onDemand != "" && len(globs) == 0 {
		f.Note = onDemand + "; here it always applies"
	}
	return f
}

// one is a FolderPattern Read for a reader that turns a file into one item.
func one(fn func(f importer.FolderFile) importer.Found) func(importer.FolderFile) []importer.Found {
	return func(f importer.FolderFile) []importer.Found { return []importer.Found{fn(f)} }
}
