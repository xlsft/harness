package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SendUserFile limits.
const (
	// MaxSentFileBytes is the largest file that can be sent (and read back with ReadSentFile).
	MaxSentFileBytes = 50 << 20
	maxSentFiles     = 10
	maxSentCaption   = 1000
	// maxSentFilesSynced is how many bytes of one call travel inside its result when the server has no object store (or
	// there is no server): the result is then the only thing that reaches another device (live over the relay, durably
	// in the uploaded transcript). Same size as a Visual's HTML, which takes the same way.
	maxSentFilesSynced = 512 << 10
)

// Kinds of sent files: the ones a side pane renders, and the rest.
const (
	sentImage    = "image"
	sentSVG      = "svg"
	sentHTML     = "html"
	sentPDF      = "pdf"
	sentMarkdown = "markdown"
	sentOther    = "file"
)

// Display of a sent file.
const (
	sentInline     = "inline"
	sentAttachment = "attachment"
)

var sentKindByExt = map[string]string{
	".png": sentImage, ".jpg": sentImage, ".jpeg": sentImage, ".gif": sentImage, ".webp": sentImage, ".avif": sentImage, ".bmp": sentImage,
	".svg": sentSVG, ".html": sentHTML, ".htm": sentHTML, ".pdf": sentPDF, ".md": sentMarkdown, ".markdown": sentMarkdown,
}

var sentMimeByExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp",
	".svg": "image/svg+xml", ".html": "text/html", ".htm": "text/html", ".pdf": "application/pdf", ".md": "text/markdown", ".markdown": "text/markdown",
}

// SentFileMime names the type of a file from its extension, else from its first bytes.
func SentFileMime(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	if m, ok := sentMimeByExt[ext]; ok {
		return m
	}
	if m := mime.TypeByExtension(ext); m != "" {
		return m
	}
	return http.DetectContentType(data[:min(len(data), 512)])
}

// ObjectStore takes files to the server's object store in the background (syncclient.ObjectQueue).
type ObjectStore interface {
	// Available reports whether the server has a store.
	Available() bool
	// AddSentFile queues a file as it was sent; false when it stays on this machine (a sensitive path).
	AddSentFile(sessionID, path, mediaType string, data []byte) bool
}

// sentFile is one card entry, in the call's structured result.
type sentFile struct {
	Name string `json:"name"`
	// Path is absolute, on the session's machine.
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mime    string `json:"mime"`
	Kind    string `json:"kind"`
	Display string `json:"display"`
	SHA256  string `json:"sha256"`
	// Data is the file, base64, when it fits the call's budget and the server has no store: then other devices can see it.
	Data string `json:"data,omitempty"`
	// Stored: the file went to the object store, and other devices read it from there by SHA256.
	Stored bool `json:"stored,omitempty"`
}

// SendUserFile surfaces files as cards in the transcript. The frontends read them, on other devices through the object
// store when the server has one (sensitive paths stay out of it), else through the small copies that sync with the
// transcript. Read deny rules hold.
type SendUserFile struct{}

func (*SendUserFile) Name() string                  { return "SendUserFile" }
func (*SendUserFile) ReadOnly(json.RawMessage) bool { return true }

// readSentFile validates one path against the session's scope and reads it.
func readSentFile(env *Env, raw string) ([]byte, string, error) {
	p := resolvePath(env.Cwd, strings.TrimSpace(raw))
	scope := env.WorkingDirs()
	if env.ScratchDir != "" {
		scope = append(scope, env.ScratchDir)
	}
	if !within(p, scope) {
		return nil, p, errors.New("outside the session's working directories")
	}
	if env.ReadDenied != nil && env.ReadDenied(p) {
		return nil, p, errors.New("reading it is denied by a permission rule")
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, p, errors.New("no such file")
	}
	if !st.Mode().IsRegular() {
		return nil, p, errors.New("not a regular file")
	}
	if st.Size() > MaxSentFileBytes {
		return nil, p, fmt.Errorf("%d MB; the limit is %d MB", st.Size()>>20, MaxSentFileBytes>>20)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, p, errors.New("cannot be read")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxSentFileBytes+1))
	if err != nil {
		return nil, p, errors.New("cannot be read")
	}
	if len(data) > MaxSentFileBytes {
		return nil, p, fmt.Errorf("larger than the limit of %d MB", MaxSentFileBytes>>20)
	}
	return data, p, nil
}

func (*SendUserFile) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Files   []string `json:"files"`
		Caption string   `json:"caption"`
		Status  string   `json:"status"`
		Display string   `json:"display"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	caption := strings.TrimSpace(in.Caption)
	switch {
	case len(in.Files) == 0:
		return ErrorResult("InputValidationError: files needs at least one path")
	case len(in.Files) > maxSentFiles:
		return ErrorResult("InputValidationError: at most %d files per call", maxSentFiles)
	case in.Status != "normal" && in.Status != "proactive":
		return ErrorResult("InputValidationError: status must be \"normal\" or \"proactive\"")
	case in.Display != "" && in.Display != "auto" && in.Display != sentInline && in.Display != sentAttachment:
		return ErrorResult("InputValidationError: display must be \"auto\", \"inline\" or \"attachment\"")
	case len([]rune(caption)) > maxSentCaption:
		return ErrorResult("InputValidationError: caption is longer than %d characters", maxSentCaption)
	}

	// All or nothing: a refused path refuses the call, with every reason at once.
	var refused []string
	var files []sentFile
	seen := map[string]bool{}
	budget := maxSentFilesSynced
	for _, raw := range in.Files {
		data, p, err := readSentFile(env, raw)
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		sum := sha256.Sum256(data)
		kind, ok := sentKindByExt[strings.ToLower(filepath.Ext(p))]
		if !ok {
			kind = sentOther
		}
		display := sentInline
		if in.Display == sentAttachment || kind == sentOther {
			display = sentAttachment
		}
		f := sentFile{Name: filepath.Base(p), Path: p, Size: int64(len(data)), Mime: SentFileMime(p, data), Kind: kind, Display: display, SHA256: hex.EncodeToString(sum[:])}
		if env.Objects != nil && env.Objects.Available() {
			f.Stored = env.Objects.AddSentFile(env.SessionID, p, f.Mime, data)
		} else if len(data) <= budget {
			budget -= len(data)
			f.Data = base64.StdEncoding.EncodeToString(data)
		}
		files = append(files, f)
	}
	if len(refused) > 0 {
		return ErrorResult("Nothing was sent. Refused: %s", strings.Join(refused, "; "))
	}

	shown := false
	if h := env.Host(); h != nil {
		if r, ok := h.Interactor().(HTMLRenderer); ok {
			shown = r.RendersHTML()
		}
	}
	structured := map[string]any{"files": files, "caption": caption, "status": in.Status, "shown": shown}
	if !shown {
		r := TextResult("Nothing was shown: no interface that displays files is attached to this session. Name the paths in your reply instead.", structured)
		r.Summary = "Not shown (no UI)"
		return r
	}
	var localOnly []string
	for _, f := range files {
		if f.Data == "" && !f.Stored {
			localOnly = append(localOnly, f.Name)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Shown to the user as %d %s in the transcript.", len(files), countWord(len(files), "card", "cards"))
	if len(localOnly) > 0 {
		fmt.Fprintf(&b, " Too large to reach their other devices, so viewable on this machine only: %s.", strings.Join(localOnly, ", "))
	}
	summary := "Sent " + files[0].Name
	if len(files) > 1 {
		summary = fmt.Sprintf("Sent %d files", len(files))
	}
	if in.Status == "proactive" {
		body := caption
		if body == "" {
			body = strings.TrimPrefix(summary, "Sent ")
		}
		if r := []rune(oneLine(body)); len(r) > maxPushBody {
			body = string(r[:maxPushBody])
		}
		outcome, _ := pushToUser(env, call.ID, "A file is ready", oneLine(body))
		structured["notified"] = outcome == pushSent
	}
	r := TextResult(b.String(), structured)
	r.Summary = summary
	return r
}
