package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Write creates or overwrites a file. Overwriting requires a prior Read.
type Write struct{}

type writeInput struct {
	FilePath string  `json:"file_path"`
	Content  *string `json:"content"`
}

func (*Write) Name() string                  { return "Write" }
func (*Write) ReadOnly(json.RawMessage) bool { return false }

// FilePath extracts file_path from an input (used by permissions).
func FilePath(input json.RawMessage) string {
	var v struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
	}
	_ = json.Unmarshal(input, &v)
	if v.FilePath != "" {
		return v.FilePath
	}
	if v.NotebookPath != "" {
		return v.NotebookPath
	}
	return v.Path
}

func (*Write) Run(ctx context.Context, env *Env, call Call) Result {
	var in writeInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.FilePath == "" || in.Content == nil {
		return ErrorResult("InputValidationError: file_path and content are required")
	}
	if !filepath.IsAbs(in.FilePath) {
		return ErrorResult("file_path must be an absolute path, got %q", in.FilePath)
	}
	path := filepath.Clean(in.FilePath)
	st, statErr := os.Stat(path)
	exists := statErr == nil
	if exists && st.IsDir() {
		return ErrorResult("%s is a directory", path)
	}
	if msg := env.Files.CheckWritable(path); msg != "" {
		return ErrorResult("%s", msg)
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	mode := os.FileMode(0o644)
	var original *string
	if exists {
		mode = st.Mode().Perm()
		if data, err := os.ReadFile(path); err == nil {
			o := string(data)
			original = &o
		}
	}
	env.beforeEdit(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ErrorResult("%v", err)
	}
	if err := os.WriteFile(path, []byte(*in.Content), mode); err != nil {
		return ErrorResult("%v", err)
	}
	env.Files.MarkReadContent(path, []byte(*in.Content))
	kind := "create"
	msg := fmt.Sprintf("File created successfully at: %s", path)
	oldText := ""
	if exists {
		kind = "update"
		msg = fmt.Sprintf("The file %s has been updated successfully.", path)
		if original != nil {
			oldText = *original
		}
	}
	patch := DiffLines(oldText, *in.Content, 3)
	r := TextResult(msg, map[string]any{"type": kind, "filePath": path, "content": *in.Content, "structuredPatch": nonNilHunks(patch), "originalFile": original})
	add, del := DiffStats(patch)
	r.Summary = fmt.Sprintf("Wrote %d lines", len(diffSplit(*in.Content)))
	if kind == "update" {
		r.Summary = fmt.Sprintf("Updated (+%d -%d)", add, del)
	}
	return r
}

// Edit performs exact string replacement in a file.
type Edit struct{}

type editInput struct {
	FilePath   string  `json:"file_path"`
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll bool    `json:"replace_all"`
}

func (*Edit) Name() string                  { return "Edit" }
func (*Edit) ReadOnly(json.RawMessage) bool { return false }

func (*Edit) Run(ctx context.Context, env *Env, call Call) Result {
	var in editInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.FilePath == "" || in.OldString == nil || in.NewString == nil {
		return ErrorResult("InputValidationError: file_path, old_string and new_string are required")
	}
	if !filepath.IsAbs(in.FilePath) {
		return ErrorResult("file_path must be an absolute path, got %q", in.FilePath)
	}
	oldS, newS := *in.OldString, *in.NewString
	if oldS == newS {
		return ErrorResult("No changes to make: old_string and new_string are exactly the same.")
	}
	path := filepath.Clean(in.FilePath)
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		if oldS == "" {
			// Creating a new file via Edit with empty old_string.
			env.beforeEdit(path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return ErrorResult("%v", err)
			}
			if err := os.WriteFile(path, []byte(newS), 0o644); err != nil {
				return ErrorResult("%v", err)
			}
			env.Files.MarkReadContent(path, []byte(newS))
			r := TextResult(fmt.Sprintf("File created successfully at: %s", path), map[string]any{"filePath": path, "oldString": "", "newString": newS,
				"originalFile": nil, "structuredPatch": nonNilHunks(DiffLines("", newS, 3)), "userModified": false, "replaceAll": false})
			r.Summary = "Created file"
			return r
		}
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
	content := string(data)
	if oldS == "" {
		if strings.TrimSpace(content) != "" {
			return ErrorResult("Cannot create new file - file already exists.")
		}
		content = ""
		oldS = content
	}
	var updated string
	if oldS == "" {
		updated = newS
	} else {
		n := strings.Count(content, oldS)
		if n == 0 {
			return ErrorResult("String to replace not found in file.\nString: %s", oldS)
		}
		if n > 1 && !in.ReplaceAll {
			return ErrorResult("Found %d matches of the string to replace, but replace_all is false. To replace all occurrences, set replace_all to true. To replace only one occurrence, please provide more context to uniquely identify the instance.\nString: %s", n, oldS)
		}
		if in.ReplaceAll {
			updated = strings.ReplaceAll(content, oldS, newS)
		} else {
			updated = strings.Replace(content, oldS, newS, 1)
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
	msg := fmt.Sprintf("The file %s has been updated successfully.", path)
	if in.ReplaceAll {
		msg = fmt.Sprintf("The file %s has been updated. All occurrences were successfully replaced.", path)
	} else if snip := snippet(updated, newS); snip != "" {
		msg += " Here's the result of running `cat -n` on a snippet of the edited file:\n" + snip
	}
	patch := DiffLines(string(data), updated, 3)
	r := TextResult(msg, map[string]any{"filePath": path, "oldString": *in.OldString, "newString": newS, "replaceAll": in.ReplaceAll,
		"originalFile": string(data), "structuredPatch": nonNilHunks(patch), "userModified": false})
	add, del := DiffStats(patch)
	r.Summary = fmt.Sprintf("Updated (+%d -%d)", add, del)
	return r
}

func nonNilHunks(h []Hunk) []Hunk {
	if h == nil {
		return []Hunk{}
	}
	return h
}

// snippet returns numbered lines around the first occurrence of s.
func snippet(content, s string) string {
	idx := strings.Index(content, s)
	if idx < 0 {
		return ""
	}
	startLine := strings.Count(content[:idx], "\n")
	endLine := startLine + strings.Count(s, "\n")
	lines := strings.Split(content, "\n")
	from := max(0, startLine-4)
	to := min(len(lines)-1, endLine+4)
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	return b.String()
}
