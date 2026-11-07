package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MultiEdit applies several replacements to one file as one change: all of them land or none does. Each works on the
// result of the ones before it.
type MultiEdit struct{}

type multiEditInput struct {
	FilePath string `json:"file_path"`
	Edits    []struct {
		OldString  *string `json:"old_string"`
		NewString  *string `json:"new_string"`
		ReplaceAll bool    `json:"replace_all"`
	} `json:"edits"`
}

func (*MultiEdit) Name() string                  { return "MultiEdit" }
func (*MultiEdit) ReadOnly(json.RawMessage) bool { return false }

func (*MultiEdit) Run(ctx context.Context, env *Env, call Call) Result {
	var in multiEditInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.FilePath == "" || len(in.Edits) == 0 {
		return ErrorResult("InputValidationError: file_path and at least one edit are required")
	}
	if !filepath.IsAbs(in.FilePath) {
		return ErrorResult("file_path must be an absolute path, got %q", in.FilePath)
	}
	path := filepath.Clean(in.FilePath)
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return ErrorResult("File does not exist. Current working directory: %s", env.CurrentDir())
	}
	if err != nil {
		return ErrorResult("%v", err)
	}
	if st.IsDir() {
		return ErrorResult("%s is a directory", path)
	}
	if msg := env.Files.CheckWritable(path); msg != "" {
		return ErrorResult("%s", msg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("%v", err)
	}
	updated := string(data)
	for i, e := range in.Edits {
		if e.OldString == nil || e.NewString == nil || *e.OldString == "" {
			return ErrorResult("InputValidationError: edit %d needs a non-empty old_string and a new_string. Nothing was changed.", i+1)
		}
		if *e.OldString == *e.NewString {
			return ErrorResult("Edit %d changes nothing: old_string and new_string are the same. Nothing was changed.", i+1)
		}
		n := strings.Count(updated, *e.OldString)
		if n == 0 {
			return ErrorResult("Edit %d: string to replace not found, after the edits before it. Nothing was changed.\nString: %s", i+1, *e.OldString)
		}
		if n > 1 && !e.ReplaceAll {
			return ErrorResult("Edit %d: found %d matches, but replace_all is false. Give more context to identify one, or set replace_all. Nothing was changed.\nString: %s", i+1, n, *e.OldString)
		}
		if e.ReplaceAll {
			updated = strings.ReplaceAll(updated, *e.OldString, *e.NewString)
		} else {
			updated = strings.Replace(updated, *e.OldString, *e.NewString, 1)
		}
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	env.beforeEdit(path)
	if err := os.WriteFile(path, []byte(updated), st.Mode().Perm()); err != nil {
		return ErrorResult("%v", err)
	}
	env.Files.MarkReadContent(path, []byte(updated))
	env.afterEdit(ctx, path)
	patch := DiffLines(string(data), updated, 3)
	r := TextResult(fmt.Sprintf("The file %s has been updated: %d edits applied.", path, len(in.Edits)), map[string]any{"filePath": path, "edits": len(in.Edits),
		"originalFile": string(data), "structuredPatch": nonNilHunks(patch), "userModified": false})
	add, del := DiffStats(patch)
	r.Summary = fmt.Sprintf("Updated (+%d -%d)", add, del)
	return r
}

// LSMaxEntries caps one listing.
const LSMaxEntries = 1000

// LS lists a directory: its entries, folders first, with what kind each is and how big.
type LS struct{}

func (*LS) Name() string                  { return "LS" }
func (*LS) ReadOnly(json.RawMessage) bool { return true }

func (*LS) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Path   string   `json:"path"`
		Ignore []string `json:"ignore"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	dir := env.CurrentDir()
	if in.Path != "" {
		dir = resolvePath(env.Shell.Cwd(), in.Path)
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return ErrorResult("Directory does not exist: %s", dir)
	}
	if err != nil {
		return ErrorResult("%v", err)
	}
	type row struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Size int64  `json:"size,omitempty"`
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil {
			return ErrorResult("interrupted")
		}
		name := e.Name()
		if vcsDirs[name] || ignoredName(in.Ignore, name) {
			continue
		}
		r := row{Name: name, Kind: "file"}
		switch t := e.Type(); {
		case t.IsDir():
			r.Kind = "dir"
		case t&fs.ModeSymlink != 0:
			r.Kind = "link"
		case !t.IsRegular():
			r.Kind = "other"
		default:
			if info, err := e.Info(); err == nil {
				r.Size = info.Size()
			}
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if (rows[i].Kind == "dir") != (rows[j].Kind == "dir") {
			return rows[i].Kind == "dir"
		}
		return rows[i].Name < rows[j].Name
	})
	total := len(rows)
	cut := total > LSMaxEntries
	if cut {
		rows = rows[:LSMaxEntries]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", dir)
	if total == 0 {
		b.WriteString("(empty)\n")
	}
	for _, r := range rows {
		switch r.Kind {
		case "dir":
			fmt.Fprintf(&b, "%s/\n", r.Name)
		case "link":
			fmt.Fprintf(&b, "%s@\n", r.Name)
		case "other":
			fmt.Fprintf(&b, "%s\n", r.Name)
		default:
			fmt.Fprintf(&b, "%s  %d\n", r.Name, r.Size)
		}
	}
	if cut {
		fmt.Fprintf(&b, "(%d more not shown: use Glob to narrow it down)\n", total-LSMaxEntries)
	}
	res := TextResult(strings.TrimRight(b.String(), "\n"), map[string]any{"path": dir, "entries": rows, "total": total})
	res.Summary = fmt.Sprintf("%d entries", total)
	return res
}

func ignoredName(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}
