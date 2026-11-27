package sources

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/memory"
)

// Building blocks of the user-level readers: files in the user's home, read with a size bound, never through a link.

// maxUserFile bounds every user-level file a reader reads.
const maxUserFile = memory.MaxFileBytes

// maxSkillFiles bounds a skill's extra files.
const maxSkillFiles = 200

// readUser reads a regular file of at most maxUserFile bytes. ok is false when it is missing; err when it is a link,
// too large or unreadable.
func readUser(p string) (data []byte, ok bool, err error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, false, nil
	}
	if !st.Mode().IsRegular() {
		return nil, true, errNotRegular
	}
	if st.Size() > maxUserFile {
		return nil, true, errTooLarge
	}
	data, err = os.ReadFile(p)
	return data, true, err
}

type readErr string

func (e readErr) Error() string { return string(e) }

const (
	errNotRegular = readErr("not a regular file (links are not followed)")
	errTooLarge   = readErr("larger than an instruction file may be")
)

// failed is an item for a file that exists but could not be read.
func failed(env importer.Env, kind importer.Kind, name, p string, err error) importer.Found {
	return importer.Found{Kind: kind, Name: name, Source: env.Display(p), Status: importer.StatusError, Note: err.Error()}
}

// userInstructions reads a whole file as always-on instructions named name; nothing when it is missing or blank.
func userInstructions(env importer.Env, p, name string) []importer.Found {
	data, ok, err := readUser(p)
	if !ok {
		return nil
	}
	if err != nil {
		return []importer.Found{failed(env, importer.KindInstructions, name, p, err)}
	}
	text := strings.TrimSpace(body(string(data)))
	if text == "" {
		return nil
	}
	return []importer.Found{{Kind: importer.KindInstructions, Name: name, Source: env.Display(p), Text: text}}
}

// userMCP reads a JSON (jsonc: with comments) file of MCP servers under key.
func userMCP(env importer.Env, p, key string, jsonc bool) []importer.Found {
	data, ok, err := readUser(p)
	if !ok {
		return nil
	}
	if err != nil {
		return []importer.Found{failed(env, importer.KindMCP, path.Base(filepath.ToSlash(p)), p, err)}
	}
	return mcpFile(data, key, env.Display(p), jsonc)
}

// userFiles lists the regular files below dir whose names end in ext (case-insensitive), as slash paths relative to dir,
// sorted. recursive also walks subfolders; hidden ones (".system") are left out.
func userFiles(dir, ext string, recursive bool) []string {
	var out []string
	ext = strings.ToLower(ext)
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (!recursive || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(d.Name()), ext) {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

// userMarkdown reads every file ending in ext below dir as one item each. name turns the relative path into the item's
// name; convert turns the file into the item's text, with an optional note ("" text skips the file as empty).
func userMarkdown(env importer.Env, dir, ext string, recursive bool, kind importer.Kind, name func(rel string) string, convert func(md string) (string, string)) []importer.Found {
	var out []importer.Found
	for _, rel := range userFiles(dir, ext, recursive) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		n := name(rel)
		data, _, err := readUser(p)
		if err != nil {
			out = append(out, failed(env, kind, n, p, err))
			continue
		}
		f := importer.Found{Kind: kind, Name: n, Source: env.Display(p), Text: string(data)}
		if convert != nil {
			f.Text, f.Note = convert(string(data))
		}
		out = append(out, f)
	}
	return out
}

// trimExt drops ext (case-insensitive) from a relative path.
func trimExt(rel, ext string) string {
	if strings.HasSuffix(strings.ToLower(rel), strings.ToLower(ext)) {
		return rel[:len(rel)-len(ext)]
	}
	return rel
}

// userSkills reads every <dir>/<name>/SKILL.md with the folder's other files. Hidden folders (Codex's bundled
// ".system") are left out.
func userSkills(env importer.Env, dir string) []importer.Found {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []importer.Found
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(dir, e.Name())
		p := filepath.Join(skillDir, "SKILL.md")
		data, ok, err := readUser(p)
		if !ok {
			continue
		}
		if err != nil {
			out = append(out, failed(env, importer.KindSkill, e.Name(), p, err))
			continue
		}
		files, skipped := skillTree(skillDir)
		f := importer.Found{Kind: importer.KindSkill, Name: e.Name(), Source: env.Display(skillDir), Text: string(data), Files: files}
		if len(skipped) > 0 {
			f.Note = "left out: " + strings.Join(skipped, ", ")
		}
		out = append(out, f)
	}
	return out
}

// skillTree reads a skill's files other than SKILL.md; links, oversized files and anything past maxSkillFiles are named in
// skipped.
func skillTree(dir string) (files []importer.File, skipped []string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "SKILL.md" {
			return nil
		}
		if !d.Type().IsRegular() || len(files) >= maxSkillFiles {
			skipped = append(skipped, rel)
			return nil
		}
		st, err := d.Info()
		if err != nil || st.Size() > maxUserFile {
			skipped = append(skipped, rel)
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			skipped = append(skipped, rel)
			return nil
		}
		files = append(files, importer.File{Path: rel, Data: data, Executable: st.Mode()&0o111 != 0})
		return nil
	})
	return files, skipped
}

// frontmatter builds a markdown document: YAML frontmatter of the non-empty fields, in order, then the body.
func frontmatter(fields [][2]string, lists map[string][]string, body string) string {
	var b strings.Builder
	var any bool
	for _, kv := range fields {
		if kv[1] != "" {
			any = true
		}
	}
	if any || len(lists) > 0 {
		b.WriteString("---\n")
		for _, kv := range fields {
			if kv[1] != "" {
				b.WriteString(kv[0] + ": " + yamlString(kv[1]) + "\n")
			}
		}
		keys := sortedNames(lists)
		for _, k := range keys {
			b.WriteString(k + ":\n")
			for _, v := range lists[k] {
				b.WriteString("  - " + yamlString(v) + "\n")
			}
		}
		b.WriteString("---\n\n")
	}
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

// yamlString quotes a scalar when YAML would read it as something else.
func yamlString(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if s == "" || strings.ContainsAny(s, ":#{}[],&*!|>'\"%@`") || strings.TrimSpace(s) != s {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	return s
}

// vscodeGlobalStorage is an extension's globalStorage folder in each VS Code family editor that exists on this machine:
// VS Code, Insiders, VSCodium, Cursor and Windsurf all keep it under <config>/<app>/User/globalStorage/<extension id>.
func vscodeGlobalStorage(env importer.Env, extension string) []string {
	var out []string
	for _, app := range []string{"Code", "Code - Insiders", "VSCodium", "Cursor", "Windsurf"} {
		p := filepath.Join(env.UserConfigDir(), app, "User", "globalStorage", extension)
		if exists(p) {
			out = append(out, p)
		}
	}
	return out
}

// userRules reads the rule files below dir (recursive) with the given extensions, each limited by `paths:` frontmatter;
// prefix goes before each name ("" or "<mode>/"), note on each item.
func userRules(env importer.Env, dir, prefix, note string, exts ...string) []importer.Found {
	var out []importer.Found
	for _, ext := range exts {
		for _, rel := range userFiles(dir, ext, true) {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			name := prefix + trimExt(rel, ext)
			data, _, err := readUser(p)
			if err != nil {
				out = append(out, failed(env, importer.KindRule, name, p, err))
				continue
			}
			md := string(data)
			f := rule(name, md, globList(md, "paths"), "")
			f.Source, f.Note = env.Display(p), note
			out = append(out, f)
		}
	}
	return out
}

// dedupe marks every item after the first of the same kind and name skipped, so two files naming one MCP server (or
// command) do not both import.
func dedupe(items []importer.Found) []importer.Found {
	seen := map[string]string{}
	for i := range items {
		f := &items[i]
		if f.Status != "" {
			continue
		}
		key := string(f.Kind) + "/" + f.Name
		if first, ok := seen[key]; ok {
			f.Status, f.Note = importer.StatusSkipped, joinNotes(f.Note, "also in "+first+", which is imported")
			continue
		}
		seen[key] = f.Source
	}
	return items
}

// noted is an item to set up by hand.
func noted(kind importer.Kind, name, source, note string) importer.Found {
	return importer.Found{Kind: kind, Name: name, Source: source, Status: importer.StatusNoted, Note: note}
}

// sseByDefault marks servers with a bare url and no type as SSE, for tools that mean SSE by it (Cline).
func sseByDefault(items []importer.Found) []importer.Found {
	for _, f := range items {
		if m := f.MCP; m != nil && m.Transport == "" && m.Command == "" && m.URL != "" {
			m.Transport = "sse"
		}
	}
	return items
}

// readDirNames lists the names of dir's subfolders, sorted.
func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
