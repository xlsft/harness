package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/xlsft/harness/packages/client/llm"
)

// Read limits.
const (
	ReadDefaultLimit   = 2000
	ReadMaxLineChars   = 2000
	ReadMaxOutputBytes = 256 * 1024
	ReadMaxImageBytes  = 5 * 1024 * 1024
	ReadMaxPDFBytes    = 32 * 1024 * 1024
)

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp",
}

// Read reads files: text with line numbers, images as image blocks, PDFs as
// document blocks.
type Read struct{}

type readInput struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset"`
	Limit    *int   `json:"limit"`
	Pages    string `json:"pages"`
}

func (*Read) Name() string                  { return "Read" }
func (*Read) ReadOnly(json.RawMessage) bool { return true }

func (*Read) Run(ctx context.Context, env *Env, call Call) Result {
	var in readInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.FilePath == "" {
		return ErrorResult("InputValidationError: file_path is required")
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
		return ErrorResult("EISDIR: %s is a directory, not a file. Use Bash (ls) or Glob to list directory contents.", path)
	}
	ext := strings.ToLower(filepath.Ext(path))
	if mt, ok := imageTypes[ext]; ok {
		return readBinaryBlock(env, path, st.Size(), llm.BlockImage, mt, ReadMaxImageBytes)
	}
	if ext == ".pdf" {
		// TODO(M1): honor `pages` by rendering page ranges.
		return readBinaryBlock(env, path, st.Size(), llm.BlockDocument, "application/pdf", ReadMaxPDFBytes)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return ErrorResult("%s appears to be a binary file and cannot be read as text.", path)
	}
	env.Files.MarkReadContent(path, data)
	if len(data) == 0 {
		msg := env.reminder("empty_file")
		if msg == "" {
			msg = "(empty file)"
		}
		return TextResult(msg, map[string]any{"type": "text", "file": map[string]any{"filePath": path, "content": "", "numLines": 0, "startLine": 1, "totalLines": 0}})
	}

	text := string(data)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	offset := 1
	if in.Offset != nil && *in.Offset > 0 {
		offset = *in.Offset
	}
	limit := ReadDefaultLimit
	if in.Limit != nil && *in.Limit > 0 {
		limit = *in.Limit
	}
	if offset > total {
		return TextResult(fmt.Sprintf("<system-reminder>Warning: the file exists but is shorter than the provided offset (%d). The file has %d lines.</system-reminder>", offset, total), nil)
	}
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	var b strings.Builder
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > ReadMaxLineChars {
			line = truncateUTF8(line, ReadMaxLineChars) + "… [line truncated]"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
	}
	out := b.String()
	if len(out) > ReadMaxOutputBytes {
		return ErrorResult("File content (%d KB) exceeds the maximum allowed size (%d KB). Use the offset and limit parameters to read specific portions of the file, or search for specific content with Grep.", len(out)/1024, ReadMaxOutputBytes/1024)
	}
	r := TextResult(out, map[string]any{"type": "text", "file": map[string]any{
		"filePath": path, "numLines": end - offset + 1, "startLine": offset, "totalLines": total,
	}})
	r.Summary = fmt.Sprintf("Read %d lines", end-offset+1)
	return r
}

func readBinaryBlock(env *Env, path string, size int64, blockType, mediaType string, max int64) Result {
	if size > max {
		return ErrorResult("%s is too large (%d bytes, max %d).", path, size, max)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("%v", err)
	}
	env.Files.MarkRead(path)
	block := llm.ContentBlock{Type: blockType, Source: &llm.Source{Type: "base64", MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(data)}}
	kind := "image"
	if blockType == llm.BlockDocument {
		kind = "pdf"
	}
	return Result{Content: []llm.ContentBlock{block}, Structured: map[string]any{"type": kind, "file": map[string]any{"filePath": path, "originalSize": size}}}
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
