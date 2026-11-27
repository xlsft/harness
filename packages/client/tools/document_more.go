package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/documents"
)

// Pictures for DocumentEdit, conversion through LibreOffice, and OCR for DocumentRead.

// loadEditImages reads the picture files the edits name. They must be in the session's folders, readable under the
// permission rules: the call's permission check saw file_path, not these.
func loadEditImages(env *Env, edits []documents.Edit) error {
	for i := range edits {
		if edits[i].Image == "" {
			continue
		}
		b, err := readEditImage(env, edits[i].Image)
		if err != nil {
			return fmt.Errorf("edit %d: the picture %s: %v", i+1, edits[i].Image, err)
		}
		edits[i].ImageData = b
	}
	return nil
}

func readEditImage(env *Env, raw string) ([]byte, error) {
	if !filepath.IsAbs(raw) {
		return nil, errors.New("the path must be absolute")
	}
	p := filepath.Clean(raw)
	scope := env.WorkingDirs()
	if env.ScratchDir != "" {
		scope = append(scope, env.ScratchDir)
	}
	if !within(p, scope) {
		return nil, errors.New("it is outside the session's working folders")
	}
	if env.ReadDenied != nil && env.ReadDenied(p) {
		return nil, errors.New("a permission rule denies reading it")
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, errors.New("no such file")
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > documents.MaxImageBytes {
		return nil, fmt.Errorf("it is %d MB, over the limit of %d MB", st.Size()>>20, documents.MaxImageBytes>>20)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, documents.MaxImageBytes+1))
}

// conversion is one supported pair: LibreOffice's filter and the output's extension.
type conversion struct{ filter, ext string }

// conversions by source kind and output extension.
var conversions = map[[2]string]conversion{
	{"docx", ".pdf"}:  {"pdf", ".pdf"},
	{"pptx", ".pdf"}:  {"pdf", ".pdf"},
	{"docx", ".odt"}:  {"odt", ".odt"},
	{"odt", ".docx"}:  {"docx:MS Word 2007 XML", ".docx"},
	{"odt", ".pdf"}:   {"pdf", ".pdf"},
	{"xlsx", ".pdf"}:  {"pdf", ".pdf"},
	{"docx", ".docx"}: {}, // listed so the message can say "already"
}

func convertKind(p string) string {
	if strings.EqualFold(filepath.Ext(p), ".odt") {
		return "odt"
	}
	if f, ok := documents.ExtensionFormat(p); ok {
		return string(f)
	}
	return ""
}

func conversionFor(source, out string) (conversion, error) {
	c, ok := conversions[[2]string{convertKind(source), strings.ToLower(filepath.Ext(out))}]
	if !ok || c.filter == "" {
		return conversion{}, fmt.Errorf("convert does docx→pdf, pptx→pdf, xlsx→pdf, docx→odt, odt→docx and odt→pdf; not %s to %s", filepath.Ext(source), filepath.Ext(out))
	}
	return c, nil
}

func convertCommand(source, out string, c conversion) string {
	return "soffice --headless --convert-to " + shellQuote(c.filter) + " --outdir " + shellQuote(filepath.Dir(out)) + " " + shellQuote(source)
}

// PermissionHints implements PermissionHinter. A conversion that asks to leave the sandbox is checked as the command
// it is, with the user-only prompt of leaving the sandbox; the prompt names the output. Everything else is an edit of
// file_path.
func (*DocumentEdit) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	var in documentEditInput
	if json.Unmarshal(call.Input, &in) != nil || in.Operation != "convert" || !in.RunOutsideSandbox || !env.Sandbox.State().Active {
		return PermissionHints{}
	}
	c, err := conversionFor(in.Source, in.FilePath)
	if err != nil {
		return PermissionHints{}
	}
	return PermissionHints{CheckAs: "Bash", Command: convertCommand(in.Source, in.FilePath, c), OutsideSandbox: true, Label: "DocumentEdit"}
}

// documentConvert converts a file through LibreOffice on a temporary copy and writes the result to file_path, verified
// like any other write.
func documentConvert(ctx context.Context, env *Env, path string, in documentEditInput) Result {
	if in.Source == "" {
		return ErrorResult("InputValidationError: convert needs source, the file to convert; file_path is the output")
	}
	if !filepath.IsAbs(in.Source) {
		return ErrorResult("source must be an absolute path, got %q", in.Source)
	}
	c, err := conversionFor(in.Source, path)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if filepath.Clean(in.Source) == path {
		return ErrorResult("the output must be another file than the source")
	}
	outside := in.RunOutsideSandbox && env.Sandbox.State().Active
	if outside {
		// The user approved running LibreOffice unconfined; the result still lands only in the session's folders.
		scope := env.WorkingDirs()
		if env.ScratchDir != "" {
			scope = append(scope, env.ScratchDir)
		}
		if !within(path, scope) {
			return ErrorResult("%s is outside the session's working folders; a conversion outside the sandbox writes only inside them.", path)
		}
	}
	orig, mode, bad := outputState(env, path)
	if bad != nil {
		return *bad
	}
	src, err := readConvertSource(env, in.Source)
	if err != nil {
		return ErrorResult("%v", err)
	}
	eng := documentEngine(env, in.RunOutsideSandbox)
	if err := eng.Unavailable(); err != nil {
		return ErrorResult("Conversion is %v.", err)
	}
	out, err := eng.Convert(ctx, in.Source, src, c.filter, c.ext)
	if err != nil {
		return ErrorResult("%s was not written: the conversion failed: %v", path, err)
	}
	var before documents.Document
	if orig != nil {
		before, _ = openDocument(path, orig)
	}
	res := &documents.EditResult{Data: out, Changes: []string{fmt.Sprintf("Converted %s to %s with LibreOffice. Its layout can differ from Office's, and fonts missing on this machine are substituted.", in.Source, filepath.Ext(path))}}
	r := finishWrite(ctx, env, path, mode, orig, before, res, "convert")
	if m, ok := r.Structured.(map[string]any); ok {
		markOutsideSandbox(env, in.RunOutsideSandbox, m)
	}
	return r
}

// readConvertSource reads the file to convert. Office files and PDFs must have been read like any source; an
// OpenDocument file cannot be read by DocumentRead, so for it the read rules alone apply.
func readConvertSource(env *Env, raw string) ([]byte, error) {
	if convertKind(raw) != "odt" {
		return readSource(env, raw)
	}
	p := filepath.Clean(raw)
	if env.ReadDenied != nil && env.ReadDenied(p) {
		return nil, fmt.Errorf("a permission rule denies reading %s", p)
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

// documentOCR recognises text in scanned PDF pages or a picture file.
func documentOCR(ctx context.Context, env *Env, in documentReadInput) Result {
	path := filepath.Clean(in.FilePath)
	if !filepath.IsAbs(in.FilePath) {
		return ErrorResult("file_path must be an absolute path, got %q", in.FilePath)
	}
	if documents.OCR == nil {
		return ErrorResult("OCR is %v.", documents.ErrNoOCR)
	}
	var langs []string
	if in.Languages != "" {
		langs = []string{in.Languages}
	}
	if _, ok := documents.OCR.(documents.Tesseract); ok {
		var err error
		if langs, err = documents.NormalizeOCRLanguages(langs); err != nil {
			return ErrorResult("%v", err)
		}
	}
	structured := map[string]any{"type": "document", "filePath": path, "operation": "ocr", "engine": documents.OCR.Name()}
	if len(langs) > 0 {
		structured["languages"] = langs
	}
	head := fmt.Sprintf("Recognised by %s (%s)%s. Each line starts with the engine's confidence. OCR makes mistakes: check names and numbers against the page.\n", documents.OCR.Name(), documents.OCR.License(), ocrLanguageNote(langs))
	if isPicturePath(path) {
		if env.ReadDenied != nil && env.ReadDenied(path) {
			return ErrorResult("a permission rule denies reading %s", path)
		}
		data, err := readEditImage(env, path)
		if err != nil {
			return ErrorResult("%s: %v", path, err)
		}
		text, err := documents.RecognizeImage(ctx, data, langs)
		if err != nil {
			return ErrorResult("%s: %v", path, err)
		}
		r := TextResult(documents.Untrusted(path, head+text), structured)
		r.Summary = "Recognised text"
		return r
	}
	p, _, bad := documentPath(env, path)
	if bad != nil {
		return *bad
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ErrorResult("%v", err)
	}
	doc, err := openDocument(p, data)
	if err != nil {
		return ErrorResult("%s: %v", p, err)
	}
	if doc.Format() != documents.FormatPDF {
		return ErrorResult("ocr reads PDFs and pictures; %s is a %s file, whose text DocumentRead reads directly.", p, doc.Format())
	}
	env.Files.MarkReadContent(p, data)
	total, err := documents.PDFPageCount(data)
	if err != nil {
		return ErrorResult("%v", err)
	}
	var pages []int
	if spec := firstNonEmpty(in.Pages, in.Range); spec != "" {
		if pages, err = documents.ParsePagesFor(spec, total); err != nil {
			return ErrorResult("%v", err)
		}
	} else {
		pages = documents.ScannedPages(doc)
		if len(pages) == 0 {
			return ErrorResult("No page of %s looks scanned: read its text with operation read. Name pages to recognise them anyway.", p)
		}
	}
	if len(pages) > documentOCRMaxPages {
		return ErrorResult("That is %d pages; one call recognises at most %d. Name fewer pages.", len(pages), documentOCRMaxPages)
	}
	text, err := documents.RecognizePDF(ctx, data, pages, langs)
	if err != nil {
		return ErrorResult("%v", err)
	}
	structured["pages"] = pages
	r := TextResult(documents.Untrusted(p, head+text), structured)
	r.Summary = fmt.Sprintf("Recognised %d pages", len(pages))
	return r
}

// documentOCRMaxPages bounds one recognition call: each page is rendered at 300 dpi.
const documentOCRMaxPages = 20

// ocrLanguageNote names the languages recognition used, and how to ask for others.
func ocrLanguageNote(langs []string) string {
	if len(langs) == 0 {
		return ""
	}
	names := make([]string, 0, len(langs))
	for _, l := range langs {
		if k, ok := documents.OCRLanguages[l]; ok {
			names = append(names, k.Name)
		} else {
			names = append(names, l)
		}
	}
	return " for " + strings.Join(names, " and ") + " (languages: " + strings.Join(langs, "+") + "; text in another language needs it named)"
}

func isPicturePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}
