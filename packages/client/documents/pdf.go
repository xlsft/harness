package documents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/form"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// A PDF is handled at page level and through its AcroForm fields. pdfcpu does the writing. It is given untrusted bytes,
// so every call into it turns a panic into an error.

const (
	// maxPDFPagesMeasured bounds how many pages get their text measured in one structure.
	maxPDFPagesMeasured = 5000
	// scannedTextChars: a page with an image and fewer characters than this looks scanned.
	scannedTextChars = 16
)

func init() {
	// The library would otherwise write a config directory into the user's home.
	api.DisableConfigDir()
}

func pdfConf() *model.Configuration {
	c := model.NewDefaultConfiguration()
	c.ValidationMode = model.ValidationRelaxed
	return c
}

type pdfPage struct {
	number, chars, images int
	rotate                int
	width, height         float64
	text                  string
	undecodable           bool
}

type pdfField struct {
	name, typ, value, options string
	pages                     []int
	locked                    bool
}

type pdfDoc struct {
	data      []byte
	ctx       *model.Context
	pages     []*pdfPage
	measured  bool
	outline   []pdfcpu.Bookmark
	fields    []pdfField
	xfa       bool
	signed    bool
	encrypted bool
	version   string
	loadErr   string
}

func guard(what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s: cannot parse the PDF: %v", what, r)
		}
	}()
	return fn()
}

func openPDF(data []byte) (Document, error) {
	d := &pdfDoc{data: data}
	err := guard("open", func() error {
		ctx, err := api.ReadValidateAndOptimize(bytes.NewReader(data), pdfConf())
		if err != nil {
			return err
		}
		d.ctx = ctx
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("not a readable PDF: %v", err)
	}
	d.encrypted = d.ctx.Encrypt != nil
	d.version = d.ctx.HeaderVersion.String()
	d.inspectForm()
	_ = guard("outline", func() error {
		bms, err := pdfcpu.Bookmarks(d.ctx)
		if err == nil {
			d.outline = bms
		}
		return nil
	})
	_ = guard("fields", func() error {
		fs, err := api.FormFields(bytes.NewReader(data), pdfConf())
		if err != nil {
			return nil // no form, or a form the library cannot list: the report says what it found
		}
		for _, f := range fs {
			d.fields = append(d.fields, pdfField{name: f.Name, typ: f.Typ.String(), value: f.V, options: f.Opts, pages: f.Pages, locked: f.Locked})
		}
		return nil
	})
	return d, nil
}

// inspectForm looks for what must not be filled: XFA forms and signatures.
func (d *pdfDoc) inspectForm() {
	_ = guard("form", func() error {
		x := d.ctx.XRefTable
		cat, err := x.Catalog()
		if err != nil {
			return nil
		}
		if _, ok := cat.Find("Perms"); ok {
			d.signed = true
		}
		af, err := x.DereferenceDict(cat["AcroForm"])
		if err != nil || af == nil {
			return nil
		}
		if _, ok := af.Find("XFA"); ok {
			d.xfa = true
		}
		if v, ok := af.Find("SigFlags"); ok {
			if o, err := x.Dereference(v); err == nil {
				if n, ok := o.(types.Integer); ok && int(n)&1 != 0 {
					d.signed = true
				}
			}
		}
		return nil
	})
	// A filled signature always carries a byte range; this also sees what sits in old revisions.
	if bytes.Contains(d.data, []byte("/ByteRange")) && bytes.Contains(d.data, []byte("/Sig")) {
		d.signed = true
	}
}

func (d *pdfDoc) measure() {
	if d.measured {
		return
	}
	d.measured = true
	n := d.ctx.PageCount
	for i := 1; i <= n; i++ {
		p := &pdfPage{number: i}
		d.pages = append(d.pages, p)
		if i > maxPDFPagesMeasured {
			continue
		}
		err := guard("page", func() error { return d.measurePage(p) })
		if err != nil {
			p.undecodable = true
		}
	}
}

func (d *pdfDoc) measurePage(p *pdfPage) error {
	x := d.ctx.XRefTable
	dict, _, inh, err := x.PageDict(p.number, true)
	if err != nil {
		return err
	}
	if inh != nil {
		p.rotate = inh.Rotate
		if inh.MediaBox != nil {
			p.width, p.height = inh.MediaBox.Width(), inh.MediaBox.Height()
		}
	}
	_ = dict
	fonts := map[string]*pdfFont{}
	if inh != nil && inh.Resources != nil {
		res := inh.Resources
		if fd, err := x.DereferenceDict(res["Font"]); err == nil && fd != nil {
			for name, ref := range fd {
				fonts[name] = d.loadFont(ref)
			}
		}
		if xo, err := x.DereferenceDict(res["XObject"]); err == nil && xo != nil {
			for _, ref := range xo {
				if sd, _, err := x.DereferenceStreamDict(ref); err == nil && sd != nil {
					if st := sd.Subtype(); st != nil && *st == "Image" {
						p.images++
					}
				}
			}
		}
	}
	r, err := pdfcpu.ExtractPageContent(d.ctx, p.number)
	if err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	content, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return err
	}
	p.text, p.images = extractText(content, fonts, p.images, &p.undecodable)
	p.chars = len([]rune(strings.Join(strings.Fields(p.text), "")))
	return nil
}

func (d *pdfDoc) loadFont(ref types.Object) *pdfFont {
	x := d.ctx.XRefTable
	fd, err := x.DereferenceDict(ref)
	if err != nil || fd == nil {
		return nil
	}
	f := &pdfFont{}
	if st := fd.Subtype(); st != nil && *st == "Type0" {
		f.twoByte = true
	}
	if tu, ok := fd.Find("ToUnicode"); ok {
		if sd, _, err := x.DereferenceStreamDict(tu); err == nil && sd != nil {
			if err := sd.Decode(); err == nil {
				f.cmap = parseToUnicode(sd.Content)
			}
		}
	}
	return f
}

func (d *pdfDoc) Format() Format { return FormatPDF }

func (d *pdfDoc) scanned() []int {
	var out []int
	for _, p := range d.pages {
		if p.chars < scannedTextChars && p.images > 0 {
			out = append(out, p.number)
		}
	}
	return out
}

// Structure implements Document.
func (d *pdfDoc) Structure() string {
	d.measure()
	var b strings.Builder
	fmt.Fprintf(&b, "PDF %s: %d pages, %d KB.\n", d.version, d.ctx.PageCount, len(d.data)>>10)
	if d.encrypted {
		b.WriteString("It is encrypted: it can be read here but not changed.\n")
	}
	if d.signed {
		b.WriteString("It is SIGNED or certified: any change would invalidate that, so it is not changed here.\n")
	}
	if d.xfa {
		b.WriteString("It has an XFA form: such forms are reported, not filled here.\n")
	}
	if len(d.outline) > 0 {
		b.WriteString("Outline:\n")
		var walk func(items []pdfcpu.Bookmark, depth int)
		count := 0
		walk = func(items []pdfcpu.Bookmark, depth int) {
			for _, it := range items {
				if count == 100 {
					return
				}
				count++
				fmt.Fprintf(&b, "  %s%s (page %d)\n", strings.Repeat("  ", depth), oneLine(it.Title, 100), it.PageFrom)
				walk(it.Kids, depth+1)
			}
		}
		walk(d.outline, 0)
	}
	b.WriteString("Pages (characters of text, images):\n")
	for i, p := range d.pages {
		if i == 200 {
			fmt.Fprintf(&b, "  … %d more pages\n", len(d.pages)-200)
			break
		}
		fmt.Fprintf(&b, "  %d: %d chars, %d images, %.0f×%.0f pt", p.number, p.chars, p.images, p.width, p.height)
		if p.rotate != 0 {
			fmt.Fprintf(&b, ", rotated %d", p.rotate)
		}
		if p.undecodable {
			b.WriteString(", some text not decodable")
		}
		if p.chars < scannedTextChars && p.images > 0 {
			b.WriteString(" [looks scanned: no text layer; read it visually]")
		}
		b.WriteString("\n")
	}
	if s := d.scanned(); len(s) > 0 {
		fmt.Fprintf(&b, "%d of %d pages look scanned. OCR is not available here.\n", len(s), len(d.pages))
	}
	if len(d.fields) > 0 {
		fmt.Fprintf(&b, "%d form fields; read them with operation fields.\n", len(d.fields))
	}
	return clip(b.String())
}

// parsePages reads "3", "1-5" or "1,3,7-9" against a page count.
func parsePages(spec string, total int) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		from, to, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(from))
		if err != nil || a < 1 {
			return nil, refuse("pages %q: use page numbers from 1, as in \"3\", \"1-5\" or \"1,3,7-9\"", spec)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(strings.TrimSpace(to)); err != nil || b < a {
				return nil, refuse("pages %q: %q is not a range", spec, part)
			}
		}
		if b > total {
			return nil, refuse("page %d does not exist: the PDF has %d pages", b, total)
		}
		for i := a; i <= b; i++ {
			seen[i] = true
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out, nil
}

func pageStrings(pages []int) []string {
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = strconv.Itoa(p)
	}
	return out
}

// Read implements Document: the extracted text of the selected pages.
func (d *pdfDoc) Read(sel Selector) (string, error) {
	d.measure()
	pages := make([]int, len(d.pages))
	for i := range pages {
		pages[i] = i + 1
	}
	if r := strings.TrimSpace(sel.Range); r != "" {
		var err error
		if pages, err = parsePages(r, len(d.pages)); err != nil {
			return "", err
		}
	}
	var lines []string
	for _, n := range pages {
		p := d.pages[n-1]
		switch {
		case p.chars == 0 && p.images > 0:
			lines = append(lines, fmt.Sprintf("--- page %d: no text layer (%d images); read it visually ---", n, p.images))
		case p.chars == 0:
			lines = append(lines, fmt.Sprintf("--- page %d: no text ---", n))
		default:
			lines = append(lines, fmt.Sprintf("--- page %d ---\n%s", n, p.text))
		}
	}
	return pageLines("Extracted text; reading order follows the file and may differ from the layout.", lines, sel), nil
}

// Search implements Document.
func (d *pdfDoc) Search(query string, limit int) string {
	d.measure()
	if limit <= 0 {
		limit = DefaultLimit
	}
	q := strings.ToLower(query)
	var hits []string
	for _, p := range d.pages {
		for _, line := range strings.Split(p.text, "\n") {
			if containsFold(line, q) {
				hits = append(hits, fmt.Sprintf("page %d: %s", p.number, snippetAround(line, q)))
			}
		}
	}
	for _, f := range d.fields {
		if containsFold(f.name, q) || containsFold(f.value, q) {
			hits = append(hits, fmt.Sprintf("field %s = %s", f.name, oneLine(f.value, 100)))
		}
	}
	out := searchReport(query, hits, limit)
	if s := d.scanned(); len(s) > 0 {
		out += fmt.Sprintf("\n%d scanned page(s) have no text layer and were not searched: %v", len(s), s[:min(len(s), 20)])
	}
	return out
}

// Fields lists the form's fields; ok is false for a file that is not a PDF.
func Fields(doc Document) (string, bool) {
	d, ok := doc.(*pdfDoc)
	if !ok {
		return "", false
	}
	var b strings.Builder
	if d.xfa {
		b.WriteString("This PDF has an XFA form, which is not filled here. AcroForm fields, if any, are listed below for reading.\n")
	}
	if d.signed {
		b.WriteString("This PDF is signed: it is not filled here.\n")
	}
	if len(d.fields) == 0 {
		b.WriteString("No AcroForm fields.\n")
		return b.String(), true
	}
	for _, f := range d.fields {
		fmt.Fprintf(&b, "%q %s pages %v value %q", f.name, f.typ, f.pages, f.value)
		if f.options != "" {
			fmt.Fprintf(&b, " options [%s]", f.options)
		}
		if f.locked {
			b.WriteString(" [locked]")
		}
		b.WriteString("\n")
	}
	return clip(b.String()), true
}

// Check implements Document.
func (d *pdfDoc) Check() []Finding {
	var out []Finding
	err := guard("validate", func() error { return api.Validate(bytes.NewReader(d.data), pdfConf()) })
	if err != nil {
		out = append(out, Finding{SeverityError, "pdf:invalid", "pdfcpu reports the file invalid: " + oneLine(err.Error(), 300)})
	}
	if d.ctx.PageCount == 0 {
		out = append(out, Finding{SeverityError, "pdf:no-pages", "the file has no pages"})
	}
	d.measure()
	if s := d.scanned(); len(s) > 0 {
		out = append(out, Finding{SeverityUnverified, "pdf:scanned", fmt.Sprintf("%d page(s) have no text layer: their content cannot be checked as text", len(s))})
	}
	if d.xfa {
		out = append(out, Finding{SeverityWarning, "pdf:xfa", "the file has an XFA form, which viewers other than Acrobat may not show"})
	}
	if d.signed {
		out = append(out, Finding{SeverityWarning, "pdf:signed", "the file is signed; signature validity is not checked here"})
	}
	return out
}

// Project implements Document.
func (d *pdfDoc) Project() []string {
	d.measure()
	var lines []string
	for _, p := range d.pages {
		rot := ""
		if p.rotate != 0 {
			rot = fmt.Sprintf(" rotated %d", p.rotate)
		}
		lines = append(lines, fmt.Sprintf("[page %d%s] %.0f×%.0f pt, %d images", p.number, rot, p.width, p.height, p.images))
		for _, l := range strings.Split(p.text, "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	for _, f := range d.fields {
		lines = append(lines, fmt.Sprintf("[field %s] %s", f.name, f.value))
	}
	return lines
}

func (d *pdfDoc) refuseChange() error {
	switch {
	case d.signed:
		return refuse("this PDF is signed or certified: changing it would invalidate that. It was not changed")
	case d.encrypted:
		return refuse("this PDF is encrypted: it cannot be changed here")
	}
	return nil
}

// Apply implements Document: rotate, delete_pages, fill_form.
func (d *pdfDoc) Apply(edits []Edit) (*EditResult, error) {
	if err := d.refuseChange(); err != nil {
		return nil, err
	}
	res := &EditResult{}
	cur := d.data
	total := d.ctx.PageCount
	for i, ed := range edits {
		var err error
		var line string
		switch ed.Op {
		case "rotate":
			cur, line, err = pdfRotate(cur, ed, total)
		case "delete_pages":
			cur, total, line, err = pdfDeletePages(cur, ed, total)
		case "fill_form":
			var notes []string
			cur, line, notes, err = d.fill(cur, ed)
			res.Notes = append(res.Notes, notes...)
		default:
			err = refuse("unknown operation %q for a PDF; use one of: rotate, delete_pages, fill_form (merge and split are operations of the tool)", ed.Op)
		}
		if err != nil {
			return nil, fmt.Errorf("edit %d (%s): %w", i+1, ed.Op, err)
		}
		res.Changes = append(res.Changes, line)
	}
	res.Data = cur
	return res, nil
}

func pdfRotate(data []byte, ed Edit, total int) ([]byte, string, error) {
	if ed.Degrees%90 != 0 || ed.Degrees == 0 {
		return nil, "", refuse("degrees must be a non-zero multiple of 90")
	}
	sel := []string(nil)
	what := "all pages"
	if ed.Pages != "" {
		ps, err := parsePages(ed.Pages, total)
		if err != nil {
			return nil, "", err
		}
		sel = pageStrings(ps)
		what = "pages " + ed.Pages
	}
	var w bytes.Buffer
	err := guard("rotate", func() error { return api.Rotate(bytes.NewReader(data), &w, ed.Degrees, sel, pdfConf()) })
	if err != nil {
		return nil, "", err
	}
	return w.Bytes(), fmt.Sprintf("Rotated %s by %d degrees.", what, ed.Degrees), nil
}

func pdfDeletePages(data []byte, ed Edit, total int) ([]byte, int, string, error) {
	ps, err := parsePages(ed.Pages, total)
	if err != nil {
		return nil, 0, "", err
	}
	if len(ps) >= total {
		return nil, 0, "", refuse("that would delete every page")
	}
	var w bytes.Buffer
	err = guard("delete", func() error { return api.RemovePages(bytes.NewReader(data), &w, pageStrings(ps), pdfConf()) })
	if err != nil {
		return nil, 0, "", err
	}
	return w.Bytes(), total - len(ps), fmt.Sprintf("Deleted pages %s.", ed.Pages), nil
}

// fill sets form fields by full name, through the library's export and fill, and reads the result back.
func (d *pdfDoc) fill(data []byte, ed Edit) ([]byte, string, []string, error) {
	if d.xfa {
		return nil, "", nil, refuse("this PDF has an XFA form: such forms are reported, not filled here. It was not changed")
	}
	if len(ed.Fields) == 0 {
		return nil, "", nil, refuse("fill_form needs fields, by full name")
	}
	var group *form.FormGroup
	err := guard("export", func() error {
		g, err := api.ExportForm(bytes.NewReader(data), "document.pdf", pdfConf())
		group = g
		return err
	})
	if err != nil {
		return nil, "", nil, refuse("the form cannot be read: %v", err)
	}
	if group == nil || len(group.Forms) == 0 {
		return nil, "", nil, refuse("this PDF has no fillable AcroForm fields")
	}
	f := &group.Forms[0]
	names := func() []string {
		var out []string
		for _, x := range d.fields {
			out = append(out, fmt.Sprintf("%q (%s)", x.name, x.typ))
		}
		sort.Strings(out)
		return out
	}
	var notes []string
	keys := make([]string, 0, len(ed.Fields))
	for k := range ed.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := setFormField(f, k, ed.Fields[k]); err != nil {
			if r, ok := err.(*RefusedError); ok && len(r.Current) == 0 && strings.Contains(r.Message, "no field") {
				r.Current = names()
			}
			return nil, "", nil, err
		}
	}
	raw, err := json.Marshal(group)
	if err != nil {
		return nil, "", nil, err
	}
	var w bytes.Buffer
	err = guard("fill", func() error { return api.FillForm(bytes.NewReader(data), bytes.NewReader(raw), &w, pdfConf()) })
	if err != nil {
		return nil, "", nil, refuse("filling the form failed: %v", err)
	}
	// Read back: the fields must say what was asked.
	var back []form.Field
	err = guard("read back", func() error {
		fs, err := api.FormFields(bytes.NewReader(w.Bytes()), pdfConf())
		back = fs
		return err
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("the filled file cannot be read back: %v", err)
	}
	for _, k := range keys {
		for _, x := range back {
			if x.Name != k {
				continue
			}
			want := fmt.Sprint(ed.Fields[k])
			if b, ok := ed.Fields[k].(bool); ok {
				want = strconv.FormatBool(b)
			}
			if !strings.EqualFold(x.V, want) && x.V != "" {
				notes = append(notes, fmt.Sprintf("Field %q reads back as %q.", k, x.V))
			}
		}
	}
	return w.Bytes(), fmt.Sprintf("Filled %d field(s): %s.", len(keys), strings.Join(keys, ", ")), notes, nil
}

func setFormField(f *form.Form, name string, val any) error {
	str := fmt.Sprint(val)
	hits := 0
	for _, x := range f.TextFields {
		if x.Name == name {
			hits++
			if x.Locked {
				return refuse("field %q is locked", name)
			}
			if x.MaxLen > 0 && len([]rune(str)) > x.MaxLen {
				return refuse("field %q holds at most %d characters", name, x.MaxLen)
			}
			x.Value = str
		}
	}
	for _, x := range f.DateFields {
		if x.Name == name {
			hits++
			if x.Locked {
				return refuse("field %q is locked", name)
			}
			x.Value = str
		}
	}
	for _, x := range f.CheckBoxes {
		if x.Name == name {
			hits++
			if x.Locked {
				return refuse("field %q is locked", name)
			}
			b, ok := val.(bool)
			if !ok {
				return refuse("field %q is a checkbox: pass true or false", name)
			}
			x.Value = b
		}
	}
	choice := func(locked bool, options []string, set func(string)) error {
		if locked {
			return refuse("field %q is locked", name)
		}
		for _, o := range options {
			if o == str {
				set(str)
				return nil
			}
		}
		return refuse("field %q has no option %q; options: %s", name, str, strings.Join(options, ", "))
	}
	for _, x := range f.RadioButtonGroups {
		if x.Name == name {
			hits++
			if err := choice(x.Locked, x.Options, func(s string) { x.Value = s }); err != nil {
				return err
			}
		}
	}
	for _, x := range f.ComboBoxes {
		if x.Name == name {
			hits++
			if x.Editable {
				if x.Locked {
					return refuse("field %q is locked", name)
				}
				x.Value = str
				continue
			}
			if err := choice(x.Locked, x.Options, func(s string) { x.Value = s }); err != nil {
				return err
			}
		}
	}
	for _, x := range f.ListBoxes {
		if x.Name == name {
			hits++
			if x.Locked {
				return refuse("field %q is locked", name)
			}
			if err := choice(false, x.Options, func(s string) { x.Values = []string{s} }); err != nil {
				return err
			}
		}
	}
	if hits == 0 {
		return &RefusedError{Message: fmt.Sprintf("the form has no field %q. Fields:", name)}
	}
	if hits > 1 {
		return refuse("the name %q fits %d fields; it is ambiguous", name, hits)
	}
	return nil
}

// MergePDFs concatenates PDFs in order. Bytes that are signed or encrypted are refused.
func MergePDFs(sources [][]byte) ([]byte, error) {
	if len(sources) == 0 {
		return nil, refuse("merge needs sources")
	}
	readers := make([]io.ReadSeeker, len(sources))
	for i, s := range sources {
		doc, err := openPDF(s)
		if err != nil {
			return nil, fmt.Errorf("source %d: %w", i+1, err)
		}
		if d := doc.(*pdfDoc); d.encrypted {
			return nil, refuse("source %d is encrypted", i+1)
		}
		readers[i] = bytes.NewReader(s)
	}
	var w bytes.Buffer
	err := guard("merge", func() error { return api.MergeRaw(readers, &w, false, pdfConf()) })
	if err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// ExtractPDFPages writes a new PDF holding the given pages.
func ExtractPDFPages(data []byte, pages string) ([]byte, int, error) {
	doc, err := openPDF(data)
	if err != nil {
		return nil, 0, err
	}
	d := doc.(*pdfDoc)
	if d.encrypted {
		return nil, 0, refuse("the source is encrypted")
	}
	ps, err := parsePages(pages, d.ctx.PageCount)
	if err != nil {
		return nil, 0, err
	}
	var w bytes.Buffer
	err = guard("split", func() error { return api.Trim(bytes.NewReader(data), &w, pageStrings(ps), pdfConf()) })
	if err != nil {
		return nil, 0, err
	}
	return w.Bytes(), len(ps), nil
}

// PDFPageCount opens a PDF only to count its pages.
func PDFPageCount(data []byte) (n int, err error) {
	err = guard("count", func() error {
		c, e := api.PageCount(bytes.NewReader(data), pdfConf())
		n = c
		return e
	})
	return n, err
}
