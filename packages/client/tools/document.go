package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/documents"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/sandbox"
)

// Document tools: DocumentRead looks, DocumentEdit changes. The format code is package documents; here are the session's
// rules around it: read-before-write, the rewind snapshot, the atomic write, the verification report, the image budget.

const (
	// DocumentRenderMaxPages is how many pages one detailed render returns.
	DocumentRenderMaxPages = 4
	// DocumentImageBudget is how many images one session gets from renders; a text fallback continues past it.
	DocumentImageBudget = 40
	// documentAddressLines bounds the changed addresses an edit reports.
	documentAddressLines = 30
	// documentMaxSources bounds a merge.
	documentMaxSources = 100
)

// DocumentRead reads Office files and PDFs: structure first, then ranges.
type DocumentRead struct{}

type documentReadInput struct {
	FilePath  string `json:"file_path"`
	Operation string `json:"operation"`
	Range     string `json:"range"`
	Query     string `json:"query"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	Pages     string `json:"pages"`
	UseEngine bool   `json:"use_engine"`
	TextOnly  bool   `json:"text_only"`
	// RunOutsideSandbox: see Bash's. LibreOffice cannot run under the macOS sandbox, so this is the way to it there.
	RunOutsideSandbox bool `json:"run_outside_sandbox"`
}

func (*DocumentRead) Name() string                  { return "DocumentRead" }
func (*DocumentRead) ReadOnly(json.RawMessage) bool { return true }

// PermissionHints implements PermissionHinter: asking to run the engine outside the sandbox is checked as the command it
// is, with the user-only prompt that goes with leaving the sandbox.
func (*DocumentRead) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	var in documentReadInput
	if json.Unmarshal(call.Input, &in) != nil || !in.RunOutsideSandbox || !env.Sandbox.State().Active {
		return PermissionHints{}
	}
	if f, ok := documents.ExtensionFormat(in.FilePath); !ok || f == documents.FormatPDF {
		return PermissionHints{}
	}
	if in.Operation != "render" && !(in.Operation == "check" && in.UseEngine) {
		return PermissionHints{}
	}
	return PermissionHints{CheckAs: "Bash", Command: "soffice --headless --convert-to pdf " + shellQuote(in.FilePath), OutsideSandbox: true, Label: "DocumentRead"}
}

func documentPath(env *Env, raw string) (string, os.FileInfo, *Result) {
	fail := func(format string, args ...any) (string, os.FileInfo, *Result) {
		r := ErrorResult(format, args...)
		return "", nil, &r
	}
	if raw == "" {
		return fail("InputValidationError: file_path is required")
	}
	if !filepath.IsAbs(raw) {
		return fail("file_path must be an absolute path, got %q", raw)
	}
	path := filepath.Clean(raw)
	if _, ok := documents.ExtensionFormat(path); !ok {
		return fail("%s is not a .docx, .xlsx, .pptx or .pdf file (or one of their macro and template variants).", path)
	}
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fail("File does not exist. Current working directory: %s", env.CurrentDir())
	}
	if err != nil {
		return fail("%v", err)
	}
	if st.IsDir() {
		return fail("%s is a directory", path)
	}
	if st.Size() > documents.MaxFileBytes {
		return fail("%s is %d MB, over the limit of %d MB.", path, st.Size()>>20, documents.MaxFileBytes>>20)
	}
	return path, st, nil
}

// openDocument opens untrusted bytes: a panic in parsing is an error.
func openDocument(name string, data []byte) (doc documents.Document, err error) {
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, fmt.Errorf("cannot read it: %v", r)
		}
	}()
	return documents.Open(name, data)
}

func (*DocumentRead) Run(ctx context.Context, env *Env, call Call) Result {
	var in documentReadInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	path, _, bad := documentPath(env, in.FilePath)
	if bad != nil {
		return *bad
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	doc, err := openDocument(path, data)
	if err != nil {
		return ErrorResult("%s: %v", path, err)
	}
	op := in.Operation
	if op == "" {
		op = "structure"
	}
	env.Files.MarkReadContent(path, data)
	structured := map[string]any{"type": "document", "filePath": path, "operation": op, "format": string(doc.Format())}
	text := ""
	switch op {
	case "structure":
		text = documents.Untrusted(path, doc.Structure())
	case "read":
		s, err := doc.Read(documents.Selector{Range: firstNonEmpty(in.Range, in.Pages), Offset: in.Offset, Limit: in.Limit})
		if err != nil {
			return ErrorResult("%v", err)
		}
		text = documents.Untrusted(path, s)
	case "search":
		if in.Query == "" {
			return ErrorResult("InputValidationError: search needs query")
		}
		text = documents.Untrusted(path, doc.Search(in.Query, in.Limit))
	case "fields":
		s, ok := documents.Fields(doc)
		if !ok {
			return ErrorResult("fields is for PDF forms; %s is not a PDF.", path)
		}
		text = documents.Untrusted(path, s)
	case "check":
		text = documents.Untrusted(path, documentCheck(ctx, env, path, data, doc, in.UseEngine, in.RunOutsideSandbox))
		if in.UseEngine {
			markOutsideSandbox(env, in.RunOutsideSandbox, structured)
		}
	case "render":
		return documentRender(ctx, env, path, data, doc, in)
	default:
		return ErrorResult("InputValidationError: operation must be structure, read, search, fields, check or render, got %q", op)
	}
	r := TextResult(text, structured)
	r.Summary = "Read document: " + op
	return r
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// documentEngine builds the optional engine for this session. On macOS LibreOffice cannot run under the sandbox (see
// docs/design/documents.md), so with the sandbox active it does not start at all, unless the call asked to leave the
// sandbox, which the permission check has put to the user by now.
func documentEngine(env *Env, outside bool) *documents.Engine {
	dir := filepath.Join(env.ScratchDir, "document-engine")
	if env.ScratchDir == "" {
		dir = filepath.Join(os.TempDir(), "harness-document-engine")
	}
	e := &documents.Engine{Dir: dir}
	st := env.Sandbox.State()
	switch {
	case st.Refusing:
		e.Blocked = st.Reason
		return e
	case !st.Active || outside:
		return e
	case runtime.GOOS == "darwin":
		e.Blocked = "LibreOffice cannot run under this session's command sandbox on macOS. Pass run_outside_sandbox to ask the user to allow it on a temporary copy"
		if st.Settings.OutsideRefused {
			e.Blocked = "LibreOffice cannot run under the command sandbox on macOS, and a policy refuses running outside it"
		}
		return e
	}
	if env.Sandbox != nil {
		e.Confine = func(argv []string) ([]string, []string, error) {
			w, err := env.Sandbox.Wrap(sandbox.Request{Kind: sandbox.KindCommand, WorkDirs: env.WorkingDirs(), ScratchDir: env.ScratchDir}, argv)
			if err != nil {
				return nil, nil, err
			}
			if w == nil {
				return nil, nil, nil
			}
			return w.Argv, w.Env, nil
		}
	}
	return e
}

func documentCheck(ctx context.Context, env *Env, path string, data []byte, doc documents.Document, useEngine, outside bool) string {
	v := documents.CheckOne(doc)
	var extra []string
	if useEngine {
		eng := documentEngine(env, outside)
		switch {
		case doc.Format() == documents.FormatPDF:
			extra = append(extra, "The engine adds nothing for a PDF: render it to look at it.")
		case !eng.Available():
			extra = append(extra, "Engine check: "+eng.Unavailable().Error()+".")
			v.Unverified = append(v.Unverified, documents.Finding{Severity: documents.SeverityUnverified, Key: "engine:none", Message: "no conversion or recalculation ran, because no engine is available"})
		case doc.Format() == documents.FormatXLSX:
			fs, err := eng.Recalculate(ctx, path, data)
			if err != nil {
				v.Unverified = append(v.Unverified, documents.Finding{Severity: documents.SeverityUnverified, Key: "engine:recalc", Message: "recalculation failed: " + err.Error()})
			} else {
				v.Inherited = append(v.Inherited, fs...)
				extra = append(extra, fmt.Sprintf("LibreOffice opened a copy and recalculated it: %d stored result(s) differ.", len(fs)))
			}
		default:
			if _, err := eng.ToPDF(ctx, path, data); err != nil {
				v.Unverified = append(v.Unverified, documents.Finding{Severity: documents.SeverityUnverified, Key: "engine:convert", Message: "the engine could not convert a copy: " + err.Error()})
			} else {
				extra = append(extra, "LibreOffice opened a copy and converted it to PDF. That is not Office, but it is a second reader.")
			}
		}
	} else if doc.Format() == documents.FormatXLSX {
		extra = append(extra, "Stored formula results were not recalculated; pass use_engine to do that if the engine is available.")
	}
	out := v.Report(true)
	if len(extra) > 0 {
		out = strings.Join(extra, "\n") + "\n" + out
	}
	return out
}

// imageBudget counts the images a session got from renders.
type imageBudget struct {
	mu   sync.Mutex
	used int
}

var documentImages sync.Map // session id -> *imageBudget

func (env *Env) takeImages(n int) int {
	v, _ := documentImages.LoadOrStore(env.SessionID, &imageBudget{})
	b := v.(*imageBudget)
	b.mu.Lock()
	defer b.mu.Unlock()
	left := DocumentImageBudget - b.used
	if left <= 0 {
		return 0
	}
	if n > left {
		n = left
	}
	b.used += n
	return n
}

// documentRender shows pages: an overview at low resolution unless pages are named, with their text always alongside
// for a model that cannot see images, and in place of images past the budget.
func documentRender(ctx context.Context, env *Env, path string, data []byte, doc documents.Document, in documentReadInput) Result {
	pdf := data
	if doc.Format() != documents.FormatPDF {
		eng := documentEngine(env, in.RunOutsideSandbox)
		if err := eng.Unavailable(); err != nil {
			return ErrorResult("Rendering is %v. Use structure and read for the text.", err)
		}
		out, err := eng.ToPDF(ctx, path, data)
		if err != nil {
			return ErrorResult("Rendering failed; nothing was checked visually: %v", err)
		}
		pdf = out
	}
	total, err := documents.PDFPageCount(pdf)
	if err != nil {
		return ErrorResult("Rendering failed: %v", err)
	}
	spec := firstNonEmpty(in.Pages, in.Range)
	if spec != "" && strings.HasPrefix(spec, "s") {
		page := documents.SlideToPage(doc, spec)
		if page == 0 {
			return ErrorResult("%s is not a slide that gets rendered (hidden slides are left out).", spec)
		}
		spec = fmt.Sprint(page)
	}
	overview := spec == ""
	var pages []int
	dpi := documents.DetailDPI
	if overview {
		dpi = documents.OverviewDPI
		for p := 1; p <= total && p <= documents.OverviewPages; p++ {
			pages = append(pages, p)
		}
	} else {
		if pages, err = documents.ParsePagesFor(spec, total); err != nil {
			return ErrorResult("%v", err)
		}
		if len(pages) > DocumentRenderMaxPages {
			return ErrorResult("That is %d pages; one render shows at most %d. Render without pages for an overview.", len(pages), DocumentRenderMaxPages)
		}
	}
	textPart, terr := documents.PageText(pdf, pages)
	if terr != nil {
		textPart = "(no text: " + terr.Error() + ")"
	}
	head := fmt.Sprintf("%d pages when rendered.", total)
	if doc.Format() != documents.FormatPDF {
		head += " Rendered by LibreOffice, whose layout can differ from Office's, with this machine's fonts."
	}
	var blocks []llm.ContentBlock
	shown := 0
	if !in.TextOnly {
		want := len(pages)
		if overview {
			want = 1
		}
		if granted := env.takeImages(want); granted == 0 {
			head += fmt.Sprintf(" The image budget of %d per session is used up: text only.", DocumentImageBudget)
		} else {
			if granted < want {
				pages = pages[:granted]
			}
			imgs, err := documents.RenderPages(ctx, pdf, pages, dpi)
			if err != nil {
				head += " Images are not available: " + err.Error() + "."
			} else {
				if overview {
					imgs = []image.Image{documents.Overview(imgs, pages)}
					head += fmt.Sprintf(" Overview of pages 1-%d at low resolution; ask for pages to see them closer.", len(pages))
				}
				for _, m := range imgs {
					b, err := documents.EncodePNG(m)
					if err != nil {
						continue
					}
					blocks = append(blocks, llm.ContentBlock{Type: llm.BlockImage, Source: &llm.Source{Type: "base64", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(b)}})
					shown++
				}
			}
		}
	}
	content := append([]llm.ContentBlock{llm.TextBlock(documents.Untrusted(path, head+"\nText of these pages, for a model without vision:\n"+textPart))}, blocks...)
	structured := map[string]any{"type": "document", "filePath": path, "operation": "render", "pages": pages, "images": shown}
	if doc.Format() != documents.FormatPDF {
		markOutsideSandbox(env, in.RunOutsideSandbox, structured)
	}
	return Result{Content: content, Structured: structured, Summary: fmt.Sprintf("Rendered %d pages", len(pages))}
}

// DocumentEdit changes or creates documents.
type DocumentEdit struct{}

type documentSource struct {
	Path  string `json:"path"`
	Pages string `json:"pages"`
}

type documentEditInput struct {
	FilePath  string                `json:"file_path"`
	Operation string                `json:"operation"`
	Edits     []documents.Edit      `json:"edits"`
	Template  string                `json:"template"`
	Spec      *documents.CreateSpec `json:"spec"`
	Sources   []documentSource      `json:"sources"`
	Source    string                `json:"source"`
	Pages     string                `json:"pages"`
}

func (*DocumentEdit) Name() string                  { return "DocumentEdit" }
func (*DocumentEdit) ReadOnly(json.RawMessage) bool { return false }

func (*DocumentEdit) Run(ctx context.Context, env *Env, call Call) Result {
	var in documentEditInput
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
	if _, ok := documents.ExtensionFormat(path); !ok {
		return ErrorResult("%s is not a .docx, .xlsx, .pptx or .pdf file (or one of their macro and template variants).", path)
	}
	op := in.Operation
	if op == "" {
		op = "edit"
	}
	switch op {
	case "edit":
		return documentApply(ctx, env, path, in)
	case "create":
		return documentCreate(ctx, env, path, in)
	case "merge":
		return documentMerge(ctx, env, path, in)
	case "split":
		return documentSplit(ctx, env, path, in)
	}
	return ErrorResult("InputValidationError: operation must be edit, create, merge or split, got %q", op)
}

// refusedResult says the file was not changed. A refusal carries the current state.
func refusedResult(path string, err error) Result {
	var r *documents.RefusedError
	if errors.As(err, &r) {
		return ErrorResult("%s was not changed. %v", path, err)
	}
	return ErrorResult("%s: %v", path, err)
}

func documentApply(ctx context.Context, env *Env, path string, in documentEditInput) Result {
	if len(in.Edits) == 0 {
		return ErrorResult("InputValidationError: edit needs edits")
	}
	_, st, bad := documentPath(env, path)
	if bad != nil {
		return *bad
	}
	if msg := env.Files.CheckWritable(path); msg != "" {
		return ErrorResult("%s Use DocumentRead.", msg)
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("%v", err)
	}
	doc, err := openDocument(path, orig)
	if err != nil {
		return ErrorResult("%s: %v", path, err)
	}
	res, err := applyEdits(doc, in.Edits)
	if err != nil {
		return refusedResult(path, err)
	}
	return finishWrite(ctx, env, path, st.Mode().Perm(), orig, doc, res, "edit")
}

func applyEdits(doc documents.Document, edits []documents.Edit) (res *documents.EditResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("the edit could not be applied: %v", r)
		}
	}()
	return doc.Apply(edits)
}

// finishWrite verifies a new version on a temporary copy and only then replaces the file. New defects, a failed rewind
// snapshot or a change on disk meanwhile all leave the file as it is.
func finishWrite(ctx context.Context, env *Env, path string, mode os.FileMode, orig []byte, before documents.Document, res *documents.EditResult, kind string) Result {
	dir := env.ScratchDir
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			dir = ""
		}
	}
	v := documents.Verify(path, orig, res.Data, dir)
	if v.HasNewErrors() {
		return ErrorResult("%s was not changed: the new version would have new defects.\n%s", path, v.Report(false))
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	if msg := env.Files.CheckWritable(path); msg != "" {
		return ErrorResult("%s was not changed. %s Use DocumentRead.", path, msg)
	}
	if err := env.beforeEditChecked(path); err != nil {
		return ErrorResult("%s was not changed: its rewind snapshot failed, so this edit could not be undone: %v", path, err)
	}
	if err := documents.WriteAtomic(path, res.Data, mode); err != nil {
		return ErrorResult("%s was not changed: %v", path, err)
	}
	env.Files.MarkReadContent(path, res.Data)
	env.afterEdit(ctx, path)

	var b strings.Builder
	fmt.Fprintf(&b, "Updated %s.\n", path)
	for _, c := range res.Changes {
		b.WriteString(c + "\n")
	}
	for _, n := range res.Notes {
		b.WriteString("Note: " + n + "\n")
	}
	after, _ := openDocument(path, res.Data)
	if before != nil && after != nil {
		if lines := documents.ChangedAddresses(before, after, documentAddressLines); len(lines) > 0 {
			b.WriteString("Current addresses of what changed:\n")
			for _, l := range lines {
				b.WriteString("  " + l + "\n")
			}
		}
	}
	b.WriteString("Verification: " + v.Report(false))

	oldText, newText := "", ""
	if before != nil {
		oldText = strings.Join(before.Project(), "\n") + "\n"
	}
	if after != nil {
		newText = strings.Join(after.Project(), "\n") + "\n"
	}
	patch := DiffLines(oldText, newText, 2)
	add, del := DiffStats(patch)
	r := TextResult(documents.Untrusted(path, b.String()), map[string]any{"type": "document", "filePath": path, "operation": kind, "structuredPatch": nonNilHunks(patch)})
	r.Summary = fmt.Sprintf("Updated document (+%d -%d)", add, del)
	return r
}

func documentCreate(ctx context.Context, env *Env, path string, in documentEditInput) Result {
	var orig []byte
	mode := os.FileMode(0)
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() {
			return ErrorResult("%s is a directory", path)
		}
		if msg := env.Files.CheckWritable(path); msg != "" {
			return ErrorResult("%s already exists. %s Use DocumentRead first if you mean to replace it.", path, msg)
		}
		if b, err := os.ReadFile(path); err == nil {
			orig = b
		}
		mode = st.Mode().Perm()
	}
	var data []byte
	res := &documents.EditResult{}
	if in.Template != "" {
		if in.Spec != nil {
			return ErrorResult("InputValidationError: give template (with edits) or spec, not both")
		}
		tmpl, err := readSource(env, in.Template)
		if err != nil {
			return ErrorResult("%v", err)
		}
		a, _ := documents.ExtensionFormat(in.Template)
		z, _ := documents.ExtensionFormat(path)
		if a != z {
			return ErrorResult("the template is a %s file and %s a %s one", a, path, z)
		}
		data, err = documents.RetargetTemplate(path, tmpl)
		if err != nil {
			return ErrorResult("%s: %v", in.Template, err)
		}
		res.Changes = append(res.Changes, "Created from template "+in.Template+".")
		if len(in.Edits) > 0 {
			doc, err := openDocument(path, data)
			if err != nil {
				return ErrorResult("%s: %v", in.Template, err)
			}
			applied, err := applyEdits(doc, in.Edits)
			if err != nil {
				return refusedResult(path, err)
			}
			data = applied.Data
			res.Changes = append(res.Changes, applied.Changes...)
			res.Notes = append(res.Notes, applied.Notes...)
		}
	} else {
		if len(in.Edits) > 0 {
			return ErrorResult("InputValidationError: edits go with a template; a new file takes spec")
		}
		spec := documents.CreateSpec{}
		if in.Spec != nil {
			spec = *in.Spec
		}
		var err error
		if data, err = documents.Create(path, spec); err != nil {
			return refusedResult(path, err)
		}
		res.Changes = append(res.Changes, "Created a new document.")
	}
	res.Data = data
	var before documents.Document
	if orig != nil {
		before, _ = openDocument(path, orig)
	}
	return finishWrite(ctx, env, path, mode, orig, before, res, "create")
}

// readSource reads another file a call uses. That file is not what the permission check saw, so it must have gone
// through a read of its own.
func readSource(env *Env, raw string) ([]byte, error) {
	if !filepath.IsAbs(raw) {
		return nil, fmt.Errorf("source paths must be absolute, got %q", raw)
	}
	p := filepath.Clean(raw)
	if env.ReadDenied != nil && env.ReadDenied(p) {
		return nil, fmt.Errorf("a permission rule denies reading %s", p)
	}
	if !env.Files.WasRead(p) {
		return nil, fmt.Errorf("%s has not been read yet. Read it with DocumentRead first", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Size() > documents.MaxFileBytes {
		return nil, fmt.Errorf("%s is %d MB, over the limit of %d MB", p, st.Size()>>20, documents.MaxFileBytes>>20)
	}
	return os.ReadFile(p)
}

// outputState reads what a merge or split is about to replace.
func outputState(env *Env, path string) (orig []byte, mode os.FileMode, r *Result) {
	if st, err := os.Stat(path); err == nil {
		if msg := env.Files.CheckWritable(path); msg != "" {
			e := ErrorResult("%s already exists. %s Use DocumentRead first if you mean to replace it.", path, msg)
			return nil, 0, &e
		}
		b, err := os.ReadFile(path)
		if err == nil {
			orig = b
		}
		mode = st.Mode().Perm()
	}
	return orig, mode, nil
}

func documentMerge(ctx context.Context, env *Env, path string, in documentEditInput) Result {
	if f, _ := documents.ExtensionFormat(path); f != documents.FormatPDF {
		return ErrorResult("merge writes a PDF; %s is not one.", path)
	}
	if len(in.Sources) < 2 {
		return ErrorResult("InputValidationError: merge needs at least two sources")
	}
	if len(in.Sources) > documentMaxSources {
		return ErrorResult("merge takes at most %d sources", documentMaxSources)
	}
	orig, mode, bad := outputState(env, path)
	if bad != nil {
		return *bad
	}
	var parts [][]byte
	for _, s := range in.Sources {
		if f, ok := documents.ExtensionFormat(s.Path); !ok || f != documents.FormatPDF {
			return ErrorResult("merge takes PDF sources; %s is not one.", s.Path)
		}
		b, err := readSource(env, s.Path)
		if err != nil {
			return ErrorResult("%v", err)
		}
		if s.Pages != "" {
			if b, _, err = documents.ExtractPDFPages(b, s.Pages); err != nil {
				return ErrorResult("%s: %v", s.Path, err)
			}
		}
		parts = append(parts, b)
	}
	data, err := documents.MergePDFs(parts)
	if err != nil {
		return refusedResult(path, err)
	}
	var before documents.Document
	if orig != nil {
		before, _ = openDocument(path, orig)
	}
	return finishWrite(ctx, env, path, mode, orig, before, &documents.EditResult{Data: data, Changes: []string{fmt.Sprintf("Merged %d PDFs.", len(parts))}}, "merge")
}

func documentSplit(ctx context.Context, env *Env, path string, in documentEditInput) Result {
	if f, _ := documents.ExtensionFormat(path); f != documents.FormatPDF {
		return ErrorResult("split writes a PDF; %s is not one.", path)
	}
	if in.Source == "" || in.Pages == "" {
		return ErrorResult("InputValidationError: split needs source and pages; it writes one output, file_path, per call")
	}
	if f, ok := documents.ExtensionFormat(in.Source); !ok || f != documents.FormatPDF {
		return ErrorResult("split takes a PDF source; %s is not one.", in.Source)
	}
	orig, mode, bad := outputState(env, path)
	if bad != nil {
		return *bad
	}
	src, err := readSource(env, in.Source)
	if err != nil {
		return ErrorResult("%v", err)
	}
	data, n, err := documents.ExtractPDFPages(src, in.Pages)
	if err != nil {
		return refusedResult(path, err)
	}
	var before documents.Document
	if orig != nil {
		before, _ = openDocument(path, orig)
	}
	return finishWrite(ctx, env, path, mode, orig, before, &documents.EditResult{Data: data, Changes: []string{fmt.Sprintf("Wrote %d pages of %s.", n, in.Source)}}, "split")
}
