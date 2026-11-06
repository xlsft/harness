package tools

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/xlsft/harness/packages/client/llm"
)

// PDF limits.
const (
	// ReadPDFMaxPages is how many pages one call may ask for.
	ReadPDFMaxPages = 20
	// ReadPDFWholeMaxPages is the longest PDF read without `pages`: providers refuse longer documents.
	ReadPDFWholeMaxPages = 100
	// ReadPDFSourceMaxBytes is the largest file `pages` will open; what is sent stays under ReadMaxPDFBytes.
	ReadPDFSourceMaxBytes = 512 * 1024 * 1024
)

func init() {
	// The library would otherwise write a config directory into the user's home.
	api.DisableConfigDir()
}

func pdfConfig() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	return conf
}

// parsePDFPages reads "3", "1-5" or "1,3,7-9" into sorted, distinct page numbers, within the file's pages.
func parsePDFPages(spec string, total int) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		from, to, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(from))
		if err != nil || a < 1 {
			return nil, fmt.Errorf("pages %q: use page numbers from 1, as in \"3\", \"1-5\" or \"1,3,7-9\"", spec)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(strings.TrimSpace(to)); err != nil || b < a {
				return nil, fmt.Errorf("pages %q: %q is not a range of pages", spec, part)
			}
		}
		if a > total {
			return nil, fmt.Errorf("page %d does not exist: the PDF has %d pages", a, total)
		}
		if b > total {
			b = total
		}
		if b-a >= ReadPDFMaxPages {
			return nil, fmt.Errorf("pages %q asks for more than %d pages at once", spec, ReadPDFMaxPages)
		}
		for i := a; i <= b; i++ {
			seen[i] = true
		}
	}
	if len(seen) > ReadPDFMaxPages {
		return nil, fmt.Errorf("pages %q asks for %d pages; at most %d at once", spec, len(seen), ReadPDFMaxPages)
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out, nil
}

// pdfPageCount opens a PDF and counts its pages. The library is given untrusted bytes, so a panic is an error here.
func pdfPageCount(data []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cannot parse it: %v", r)
		}
	}()
	return api.PageCount(bytes.NewReader(data), pdfConfig())
}

func pdfSelect(data []byte, pages []int) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("cannot parse it: %v", r)
		}
	}()
	sel := make([]string, len(pages))
	for i, p := range pages {
		sel[i] = strconv.Itoa(p)
	}
	var w bytes.Buffer
	if err := api.Trim(bytes.NewReader(data), &w, sel, pdfConfig()); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// readPDF answers Read for a PDF: the whole file, or with `pages` a smaller PDF of just those pages.
func readPDF(env *Env, path string, size int64, pages string) Result {
	pages = strings.TrimSpace(pages)
	if pages == "" {
		if size > ReadMaxPDFBytes {
			return ErrorResult("%s is too large to read whole (%d MB, max %d). Pass pages, such as \"1-5\", to read part of it.", path, size>>20, ReadMaxPDFBytes>>20)
		}
	} else if size > ReadPDFSourceMaxBytes {
		return ErrorResult("%s is too large (%d MB, max %d).", path, size>>20, ReadPDFSourceMaxBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("%v", err)
	}
	env.Files.MarkRead(path)
	total, countErr := pdfPageCount(data)
	if pages == "" {
		// A file the parser cannot read may still be one the model can: only `pages` needs the parser.
		if countErr == nil && total > ReadPDFWholeMaxPages {
			return ErrorResult("%s has %d pages, too many to read at once. Pass pages, such as \"1-20\", at most %d per call.", path, total, ReadPDFMaxPages)
		}
		structured := map[string]any{"filePath": path, "originalSize": size}
		if countErr == nil {
			structured["totalPages"] = total
		}
		return Result{Content: []llm.ContentBlock{pdfBlock(data)}, Structured: map[string]any{"type": "pdf", "file": structured}}
	}
	if countErr != nil {
		return ErrorResult("%s: pages cannot be selected: %v. Read it without pages if it is small enough.", path, countErr)
	}
	selected, err := parsePDFPages(pages, total)
	if err != nil {
		return ErrorResult("%v", err)
	}
	out, err := pdfSelect(data, selected)
	if err != nil {
		return ErrorResult("%s: pages cannot be selected: %v", path, err)
	}
	if int64(len(out)) > ReadMaxPDFBytes {
		return ErrorResult("Pages %s of %s are %d MB, over the limit of %d. Ask for fewer pages.", pages, path, len(out)>>20, ReadMaxPDFBytes>>20)
	}
	note := fmt.Sprintf("Pages %s of %d of %s.", pages, total, path)
	return Result{
		Content:    []llm.ContentBlock{llm.TextBlock(note), pdfBlock(out)},
		Structured: map[string]any{"type": "pdf", "file": map[string]any{"filePath": path, "originalSize": size, "totalPages": total, "pages": selected}},
		Summary:    fmt.Sprintf("Read %d of %d pages", len(selected), total),
	}
}

func pdfBlock(data []byte) llm.ContentBlock {
	return llm.ContentBlock{Type: llm.BlockDocument, Source: &llm.Source{Type: "base64", MediaType: "application/pdf", Data: base64.StdEncoding.EncodeToString(data)}}
}
