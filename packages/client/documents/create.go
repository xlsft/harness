package documents

import (
	"archive/zip"
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Creation starts from minimal packages written here from the format specifications, filled from simple structured
// input, so a new document never needs a script. Everything is plain: design belongs to the template a caller passes.

// CreateSpec is the structured input of a new document. Which part applies depends on the format.
type CreateSpec struct {
	Title  string      `json:"title,omitempty"`
	Blocks []Block     `json:"blocks,omitempty"`
	Sheets []SheetSpec `json:"sheets,omitempty"`
	Slides []SlideSpec `json:"slides,omitempty"`
}

// Block is a block of a Word document or a PDF.
type Block struct {
	// Type is heading, paragraph, bullet, numbered, table or page_break.
	Type  string     `json:"type"`
	Level int        `json:"level,omitempty"`
	Text  string     `json:"text,omitempty"`
	Rows  [][]string `json:"rows,omitempty"`
}

// SheetSpec is a sheet of a new workbook. A string starting with = is a formula; {"date":"2026-11-07"} is a date.
type SheetSpec struct {
	Name string  `json:"name"`
	Rows [][]any `json:"rows"`
	// HeaderRow makes the first row bold.
	HeaderRow bool `json:"header_row,omitempty"`
}

// SlideSpec is a slide of a new presentation. Layout is title, content, section or blank.
type SlideSpec struct {
	Layout  string   `json:"layout,omitempty"`
	Title   string   `json:"title,omitempty"`
	Text    string   `json:"text,omitempty"`
	Bullets []string `json:"bullets,omitempty"`
	Notes   string   `json:"notes,omitempty"`
}

// Create builds a new document for a file name's format.
func Create(name string, spec CreateSpec) ([]byte, error) {
	f, ok := ExtensionFormat(name)
	if !ok {
		return nil, ErrNotDocument
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".docx", ".xlsx", ".pptx", ".pdf":
	default:
		return nil, refuse("a new file can be .docx, .xlsx, .pptx or .pdf; %s needs a template", filepath.Ext(name))
	}
	switch f {
	case FormatDOCX:
		return createDOCX(spec)
	case FormatXLSX:
		return createXLSX(spec)
	case FormatPPTX:
		return createPPTX(spec)
	case FormatPDF:
		return createPDF(spec)
	}
	return nil, ErrNotDocument
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// buildZip writes parts in order, [Content_Types].xml first as OPC readers expect.
func buildZip(parts [][2]string) ([]byte, error) {
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, p := range parts {
		fw, err := w.Create(p[0])
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write([]byte(p[1])); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

const (
	relNS     = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	pkgRelNS  = "http://schemas.openxmlformats.org/package/2006/relationships"
	ctNS      = "http://schemas.openxmlformats.org/package/2006/content-types"
	coreProps = `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>%s</dc:title></cp:coreProperties>`
)

func pkgRels(mainType, mainTarget string) string {
	return xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/` + mainType + `" Target="` + mainTarget + `"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`
}

func contentTypes(overrides ...[2]string) string {
	var b strings.Builder
	b.WriteString(xmlHeader + `<Types xmlns="` + ctNS + `"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>`)
	for _, o := range overrides {
		b.WriteString(`<Override PartName="` + o[0] + `" ContentType="` + o[1] + `"/>`)
	}
	b.WriteString(`</Types>`)
	return b.String()
}

// --- Word ---

const wNS = `xmlns:w="` + wordNSTransitional + `"`

func docxStyles() string {
	style := func(id, name string, pt, level int, bold bool, spaceBefore int) string {
		out := `<w:style w:type="paragraph" w:styleId="` + id + `"><w:name w:val="` + name + `"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="` + strconv.Itoa(spaceBefore) + `" w:after="120"/>`
		if level > 0 {
			out += `<w:outlineLvl w:val="` + strconv.Itoa(level-1) + `"/>`
		}
		out += `</w:pPr><w:rPr>`
		if bold {
			out += `<w:b/>`
		}
		return out + `<w:sz w:val="` + strconv.Itoa(pt*2) + `"/></w:rPr></w:style>`
	}
	return xmlHeader + `<w:styles ` + wNS + `><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:cs="Arial" w:eastAsia="Arial"/><w:sz w:val="22"/><w:lang w:val="en-US"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
		`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>` +
		style("Title", "Title", 26, 0, true, 0) + style("Heading1", "heading 1", 18, 1, true, 360) + style("Heading2", "heading 2", 14, 2, true, 280) + style("Heading3", "heading 3", 12, 3, true, 200) +
		`<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:qFormat/></w:style>` +
		`<w:style w:type="table" w:default="1" w:styleId="TableNormal"><w:name w:val="Normal Table"/><w:tblPr><w:tblInd w:w="0" w:type="dxa"/><w:tblCellMar><w:left w:w="108" w:type="dxa"/><w:right w:w="108" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>` +
		`<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:basedOn w:val="TableNormal"/><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:left w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:right w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideH w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="auto"/></w:tblBorders></w:tblPr></w:style>` +
		`</w:styles>`
}

func docxNumbering() string {
	return xmlHeader + `<w:numbering ` + wNS + `><w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="hybridMultilevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl></w:abstractNum>` +
		`<w:abstractNum w:abstractNumId="1"><w:multiLevelType w:val="hybridMultilevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl></w:abstractNum>` +
		`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num><w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num></w:numbering>`
}

func wRun(text string) string {
	if text == "" {
		return ""
	}
	sample := &Node{Prefix: "w"}
	return "<w:r>" + runContent(sample, text, "t") + "</w:r>"
}

func createDOCX(spec CreateSpec) ([]byte, error) {
	var body strings.Builder
	if spec.Title != "" {
		body.WriteString(`<w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr>` + wRun(spec.Title) + `</w:p>`)
	}
	for i, bl := range spec.Blocks {
		switch bl.Type {
		case "heading":
			lv := bl.Level
			if lv < 1 {
				lv = 1
			}
			if lv > 3 {
				lv = 3
			}
			fmt.Fprintf(&body, `<w:p><w:pPr><w:pStyle w:val="Heading%d"/></w:pPr>%s</w:p>`, lv, wRun(bl.Text))
		case "paragraph", "":
			body.WriteString(`<w:p>` + wRun(bl.Text) + `</w:p>`)
		case "bullet":
			body.WriteString(`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>` + wRun(bl.Text) + `</w:p>`)
		case "numbered":
			body.WriteString(`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr>` + wRun(bl.Text) + `</w:p>`)
		case "page_break":
			body.WriteString(`<w:p><w:r><w:br w:type="page"/></w:r></w:p>`)
		case "table":
			cols := 0
			for _, r := range bl.Rows {
				cols = max(cols, len(r))
			}
			if cols == 0 {
				return nil, refuse("block %d: a table needs rows", i+1)
			}
			width := 9360 / cols
			body.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="5000" w:type="pct"/></w:tblPr><w:tblGrid>`)
			for c := 0; c < cols; c++ {
				fmt.Fprintf(&body, `<w:gridCol w:w="%d"/>`, width)
			}
			body.WriteString(`</w:tblGrid>`)
			for _, r := range bl.Rows {
				body.WriteString(`<w:tr>`)
				for c := 0; c < cols; c++ {
					text := ""
					if c < len(r) {
						text = r[c]
					}
					fmt.Fprintf(&body, `<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/></w:tcPr><w:p>%s</w:p></w:tc>`, width, wRun(text))
				}
				body.WriteString(`</w:tr>`)
			}
			body.WriteString(`</w:tbl><w:p/>`)
		default:
			return nil, refuse("block %d: unknown type %q; use heading, paragraph, bullet, numbered, table or page_break", i+1, bl.Type)
		}
	}
	doc := xmlHeader + `<w:document ` + wNS + `><w:body>` + body.String() + `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`
	rels := xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/styles" Target="styles.xml"/><Relationship Id="rId2" Type="` + relNS + `/numbering" Target="numbering.xml"/><Relationship Id="rId3" Type="` + relNS + `/settings" Target="settings.xml"/></Relationships>`
	const wml = "application/vnd.openxmlformats-officedocument.wordprocessingml."
	return buildZip([][2]string{
		{contentTypesPart, contentTypes([2]string{"/word/document.xml", wml + "document.main+xml"}, [2]string{"/word/styles.xml", wml + "styles+xml"}, [2]string{"/word/numbering.xml", wml + "numbering+xml"}, [2]string{"/word/settings.xml", wml + "settings+xml"})},
		{"_rels/.rels", pkgRels("officeDocument", "word/document.xml")},
		{"docProps/core.xml", xmlHeader + fmt.Sprintf(coreProps, escText(spec.Title))},
		{"word/document.xml", doc},
		{"word/_rels/document.xml.rels", rels},
		{"word/styles.xml", docxStyles()},
		{"word/numbering.xml", docxNumbering()},
		{"word/settings.xml", xmlHeader + `<w:settings ` + wNS + `><w:defaultTabStop w:val="720"/></w:settings>`},
	})
}

// --- Excel ---

const ssNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

func createXLSX(spec CreateSpec) ([]byte, error) {
	sheets := spec.Sheets
	if len(sheets) == 0 {
		sheets = []SheetSpec{{Name: "Sheet1"}}
	}
	seen := map[string]bool{}
	overrides := [][2]string{
		{"/xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"},
		{"/xl/styles.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"},
	}
	var wbSheets, wbRels strings.Builder
	parts := [][2]string{}
	hasFormula := false
	for i, s := range sheets {
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("Sheet%d", i+1)
		}
		low := strings.ToLower(name)
		if seen[low] {
			return nil, refuse("two sheets are named %q", name)
		}
		seen[low] = true
		if len([]rune(name)) > 31 || strings.ContainsAny(name, `[]:*?/\`) {
			return nil, refuse("the sheet name %q is not allowed: at most 31 characters, none of []:*?/\\", name)
		}
		xml, f, err := sheetXML(s)
		if err != nil {
			return nil, fmt.Errorf("sheet %q: %w", name, err)
		}
		hasFormula = hasFormula || f
		part := fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		parts = append(parts, [2]string{part, xml})
		overrides = append(overrides, [2]string{"/" + part, "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"})
		fmt.Fprintf(&wbSheets, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, escAttr(name), i+1, i+1)
		fmt.Fprintf(&wbRels, `<Relationship Id="rId%d" Type="%s/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, relNS, i+1)
	}
	fmt.Fprintf(&wbRels, `<Relationship Id="rId%d" Type="%s/styles" Target="styles.xml"/>`, len(sheets)+1, relNS)
	calc := ""
	if hasFormula {
		calc = `<calcPr fullCalcOnLoad="1"/>`
	}
	wb := xmlHeader + `<workbook xmlns="` + ssNS + `" xmlns:r="` + relNS + `"><sheets>` + wbSheets.String() + `</sheets>` + calc + `</workbook>`
	styles := xmlHeader + `<styleSheet xmlns="` + ssNS + `"><fonts count="2"><font><sz val="11"/><name val="Arial"/></font><font><b/><sz val="11"/><name val="Arial"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/><xf numFmtId="14" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`
	all := [][2]string{
		{contentTypesPart, contentTypes(overrides...)},
		{"_rels/.rels", pkgRels("officeDocument", "xl/workbook.xml")},
		{"docProps/core.xml", xmlHeader + fmt.Sprintf(coreProps, escText(spec.Title))},
		{"xl/workbook.xml", wb},
		{"xl/_rels/workbook.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `">` + wbRels.String() + `</Relationships>`},
		{"xl/styles.xml", styles},
	}
	data, err := buildZip(append(all, parts...))
	if err != nil {
		return nil, err
	}
	if hasFormula {
		return storeComputedValues(data)
	}
	return data, nil
}

func sheetXML(s SheetSpec) (string, bool, error) {
	var b strings.Builder
	hasFormula := false
	maxCol, maxRow := 0, 0
	for r, row := range s.Rows {
		var cells strings.Builder
		for c, v := range row {
			ref := cellName(c+1, r+1)
			style := ""
			if s.HeaderRow && r == 0 {
				style = ` s="1"`
			}
			switch x := v.(type) {
			case nil:
				continue
			case float64:
				fmt.Fprintf(&cells, `<c r="%s"%s><v>%s</v></c>`, ref, style, fmtV(x))
			case int:
				fmt.Fprintf(&cells, `<c r="%s"%s><v>%d</v></c>`, ref, style, x)
			case bool:
				val := "0"
				if x {
					val = "1"
				}
				fmt.Fprintf(&cells, `<c r="%s"%s t="b"><v>%s</v></c>`, ref, style, val)
			case string:
				if strings.HasPrefix(x, "=") && len(x) > 1 {
					if _, err := lex(x[1:]); err != nil {
						return "", false, refuse("%s: the formula does not read: %v", ref, err)
					}
					fmt.Fprintf(&cells, `<c r="%s"%s><f>%s</f></c>`, ref, style, escText(x[1:]))
					hasFormula = true
				} else {
					fmt.Fprintf(&cells, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, escText(x))
				}
			case map[string]any:
				ds, _ := x["date"].(string)
				d := &xlsxDoc{}
				serial, ok := d.dateToSerial(ds)
				if !ok {
					return "", false, refuse("%s: a date cell is {\"date\": \"2026-11-07\"}", ref)
				}
				fmt.Fprintf(&cells, `<c r="%s" s="2"><v>%s</v></c>`, ref, fmtV(serial))
			default:
				return "", false, refuse("%s: a cell is a number, string, boolean, null or {\"date\": ...}", ref)
			}
			maxCol, maxRow = max(maxCol, c+1), max(maxRow, r+1)
		}
		if cells.Len() > 0 {
			fmt.Fprintf(&b, `<row r="%d">%s</row>`, r+1, cells.String())
		}
	}
	dim := "A1"
	if maxCol > 0 {
		dim = "A1:" + cellName(maxCol, maxRow)
	}
	return xmlHeader + `<worksheet xmlns="` + ssNS + `"><dimension ref="` + dim + `"/><sheetData>` + b.String() + `</sheetData></worksheet>`, hasFormula, nil
}

// storeComputedValues writes the values the evaluator can compute, for readers that do not calculate.
func storeComputedValues(data []byte) ([]byte, error) {
	doc, err := openXLSX(data)
	if err != nil {
		return nil, err
	}
	d := doc.(*xlsxDoc)
	affected := map[cellKey]bool{}
	for _, s := range d.sheets {
		for _, c := range s.order {
			if c.formula != "" {
				affected[cellKey{s.name, c.col, c.row}] = true
			}
		}
	}
	e := &xlsxEdit{d: d, res: &EditResult{}}
	values, _ := e.evalAll(affected)
	c := NewChanges()
	for _, s := range d.sheets {
		var spans []Span
		for _, cell := range s.order {
			if v, ok := values[cellKey{s.name, cell.col, cell.row}]; ok {
				if sp, ok := dependentSpan(s, cell, v); ok {
					spans = append(spans, sp)
				}
			}
		}
		if len(spans) > 0 {
			c.Set[s.part] = Splice(s.x.Data, spans)
		}
	}
	return d.pkg.Write(c)
}

// --- PowerPoint ---

const (
	pmlNS     = "http://schemas.openxmlformats.org/presentationml/2006/main"
	pptxNSes  = `xmlns:a="` + drawingNS + `" xmlns:r="` + relNS + `" xmlns:p="` + pmlNS + `"`
	pptSlideW = 12192000
	pptSlideH = 6858000
)

func pptxTheme() string {
	clr := func(name, hex string) string {
		return `<a:` + name + `><a:srgbClr val="` + hex + `"/></a:` + name + `>`
	}
	solid := `<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>`
	line := `<a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln>`
	return xmlHeader + `<a:theme xmlns:a="` + drawingNS + `" name="Plain"><a:themeElements><a:clrScheme name="Plain">` +
		clr("dk1", "000000") + clr("lt1", "FFFFFF") + clr("dk2", "1F2933") + clr("lt2", "F2F4F7") + clr("accent1", "2F5D8C") + clr("accent2", "C2571A") + clr("accent3", "4C7A4C") + clr("accent4", "7A4C7A") + clr("accent5", "A08A2E") + clr("accent6", "3C8C8C") + clr("hlink", "2F5D8C") + clr("folHlink", "7A4C7A") +
		`</a:clrScheme><a:fontScheme name="Plain"><a:majorFont><a:latin typeface="Arial"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont><a:minorFont><a:latin typeface="Arial"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont></a:fontScheme>` +
		`<a:fmtScheme name="Plain"><a:fillStyleLst>` + solid + solid + solid + `</a:fillStyleLst><a:lnStyleLst>` + line + line + line + `</a:lnStyleLst><a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst><a:bgFillStyleLst>` + solid + solid + solid + `</a:bgFillStyleLst></a:fmtScheme></a:themeElements></a:theme>`
}

const pptGroupHead = `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`

func phShape(id int, name, phAttrs string, g *rect, prompt string) string {
	geom := `<p:spPr/>`
	if g != nil {
		geom = fmt.Sprintf(`<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm></p:spPr>`, g.x, g.y, g.cx, g.cy)
	}
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph%s/></p:nvPr></p:nvSpPr>%s<p:txBody><a:bodyPr><a:normAutofit/></a:bodyPr><a:lstStyle/><a:p><a:r><a:rPr lang="en-US"/><a:t>%s</a:t></a:r></a:p></p:txBody></p:sp>`, id, name, phAttrs, geom, prompt)
}

func pptLayout(name, typ string, shapes string) string {
	t := ""
	if typ != "" {
		t = ` type="` + typ + `"`
	}
	return xmlHeader + `<p:sldLayout ` + pptxNSes + t + ` preserve="1"><p:cSld name="` + name + `"><p:spTree>` + pptGroupHead + shapes + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`
}

type pptLayoutDef struct {
	key, name, typ, shapes string
}

func pptLayouts() []pptLayoutDef {
	return []pptLayoutDef{
		{"title", "Title Slide", "title", phShape(2, "Title 1", ` type="ctrTitle"`, &rect{x: 914400, y: 1600200, cx: 10363200, cy: 1600200}, "Click to edit title") + phShape(3, "Subtitle 2", ` type="subTitle" idx="1"`, &rect{x: 1828800, y: 3429000, cx: 8534400, cy: 1219200}, "Click to edit subtitle")},
		{"content", "Title and Content", "obj", phShape(2, "Title 1", ` type="title"`, nil, "Click to edit title") + phShape(3, "Content 2", ` idx="1"`, nil, "Click to edit text")},
		{"section", "Section Header", "secHead", phShape(2, "Title 1", ` type="title"`, &rect{x: 838200, y: 2286000, cx: 10515600, cy: 1447800}, "Click to edit title") + phShape(3, "Text 2", ` type="body" idx="1"`, &rect{x: 838200, y: 3810000, cx: 10515600, cy: 914400}, "Click to edit text")},
		{"blank", "Blank", "blank", ""},
	}
}

func createPPTX(spec CreateSpec) ([]byte, error) {
	slides := spec.Slides
	if len(slides) == 0 {
		slides = []SlideSpec{{Layout: "title", Title: spec.Title}}
	}
	layouts := pptLayouts()
	layoutIdx := map[string]int{}
	for i, l := range layouts {
		layoutIdx[l.key] = i + 1
	}
	const pml = "application/vnd.openxmlformats-officedocument.presentationml."
	overrides := [][2]string{
		{"/ppt/presentation.xml", pml + "presentation.main+xml"},
		{"/ppt/slideMasters/slideMaster1.xml", pml + "slideMaster+xml"},
		{"/ppt/theme/theme1.xml", "application/vnd.openxmlformats-officedocument.theme+xml"},
	}
	parts := [][2]string{}
	var layoutIDs, masterRels strings.Builder
	for i, l := range layouts {
		n := i + 1
		parts = append(parts,
			[2]string{fmt.Sprintf("ppt/slideLayouts/slideLayout%d.xml", n), pptLayout(l.name, l.typ, l.shapes)},
			[2]string{fmt.Sprintf("ppt/slideLayouts/_rels/slideLayout%d.xml.rels", n), xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`})
		overrides = append(overrides, [2]string{fmt.Sprintf("/ppt/slideLayouts/slideLayout%d.xml", n), pml + "slideLayout+xml"})
		fmt.Fprintf(&layoutIDs, `<p:sldLayoutId id="%d" r:id="rId%d"/>`, 2147483649+i, n)
		fmt.Fprintf(&masterRels, `<Relationship Id="rId%d" Type="%s/slideLayout" Target="../slideLayouts/slideLayout%d.xml"/>`, n, relNS, n)
	}
	fmt.Fprintf(&masterRels, `<Relationship Id="rId%d" Type="%s/theme" Target="../theme/theme1.xml"/>`, len(layouts)+1, relNS)
	lvl := func(n int, sz int, indent bool) string {
		ind := `marL="0" indent="0"`
		bullet := `<a:buNone/>`
		if indent {
			ind = fmt.Sprintf(`marL="%d" indent="-285750"`, 285750+(n-1)*457200)
			bullet = `<a:buFont typeface="Arial"/><a:buChar char="•"/>`
		}
		return fmt.Sprintf(`<a:lvl%dpPr %s algn="l"><a:spcBef><a:spcPts val="600"/></a:spcBef>%s<a:defRPr sz="%d"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/></a:defRPr></a:lvl%dpPr>`, n, ind, bullet, sz, n)
	}
	master := xmlHeader + `<p:sldMaster ` + pptxNSes + `><p:cSld><p:bg><p:bgRef idx="1001"><a:schemeClr val="bg1"/></p:bgRef></p:bg><p:spTree>` + pptGroupHead +
		phShape(2, "Title 1", ` type="title"`, &rect{x: 838200, y: 365125, cx: 10515600, cy: 1325563}, "Click to edit title") +
		phShape(3, "Text 2", ` type="body" idx="1"`, &rect{x: 838200, y: 1825625, cx: 10515600, cy: 4351338}, "Click to edit text") +
		`</p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/><p:sldLayoutIdLst>` + layoutIDs.String() + `</p:sldLayoutIdLst><p:txStyles><p:titleStyle><a:lvl1pPr algn="l"><a:defRPr sz="4000" b="1"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/></a:defRPr></a:lvl1pPr></p:titleStyle><p:bodyStyle>` + lvl(1, 2400, true) + lvl(2, 2000, true) + lvl(3, 1800, true) + `</p:bodyStyle><p:otherStyle>` + lvl(1, 1800, false) + `</p:otherStyle></p:txStyles></p:sldMaster>`

	var sldIDs, presRels strings.Builder
	fmt.Fprintf(&presRels, `<Relationship Id="rId1" Type="%s/slideMaster" Target="slideMasters/slideMaster1.xml"/><Relationship Id="rId2" Type="%s/theme" Target="theme/theme1.xml"/>`, relNS, relNS)
	notesCount := 0
	for i, s := range slides {
		n := i + 1
		key := s.Layout
		if key == "" {
			key = "content"
		}
		li, ok := layoutIdx[key]
		if !ok {
			return nil, refuse("slide %d: unknown layout %q; use title, content, section or blank", n, s.Layout)
		}
		var sh strings.Builder
		body := s.Text
		if len(s.Bullets) > 0 {
			if body != "" {
				return nil, refuse("slide %d: give text or bullets, not both", n)
			}
			body = strings.Join(s.Bullets, "\n")
		}
		if key == "blank" {
			if s.Title != "" || body != "" {
				return nil, refuse("slide %d: the blank layout has no places for text", n)
			}
		} else {
			titleAttrs, bodyAttrs, bodyName := ` type="title"`, ` idx="1"`, "Content 2"
			switch key {
			case "title":
				titleAttrs, bodyAttrs, bodyName = ` type="ctrTitle"`, ` type="subTitle" idx="1"`, "Subtitle 2"
			case "section":
				bodyAttrs, bodyName = ` type="body" idx="1"`, "Text 2"
			}
			sh.WriteString(slideShape(2, "Title 1", titleAttrs, s.Title))
			if body != "" {
				sh.WriteString(slideShape(3, bodyName, bodyAttrs, body))
			}
		}
		parts = append(parts, [2]string{fmt.Sprintf("ppt/slides/slide%d.xml", n), xmlHeader + `<p:sld ` + pptxNSes + `><p:cSld><p:spTree>` + pptGroupHead + sh.String() + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`})
		rels := fmt.Sprintf(`<Relationship Id="rId1" Type="%s/slideLayout" Target="../slideLayouts/slideLayout%d.xml"/>`, relNS, li)
		if s.Notes != "" {
			notesCount++
			nn := notesCount
			rels += fmt.Sprintf(`<Relationship Id="rId2" Type="%s/notesSlide" Target="../notesSlides/notesSlide%d.xml"/>`, relNS, nn)
			parts = append(parts,
				[2]string{fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", nn), notesSlideXML(s.Notes)},
				[2]string{fmt.Sprintf("ppt/notesSlides/_rels/notesSlide%d.xml.rels", nn), xmlHeader + `<Relationships xmlns="` + pkgRelNS + fmt.Sprintf(`"><Relationship Id="rId1" Type="%s/notesMaster" Target="../notesMasters/notesMaster1.xml"/><Relationship Id="rId2" Type="%s/slide" Target="../slides/slide%d.xml"/></Relationships>`, relNS, relNS, n)})
			overrides = append(overrides, [2]string{fmt.Sprintf("/ppt/notesSlides/notesSlide%d.xml", nn), pml + "notesSlide+xml"})
		}
		parts = append(parts, [2]string{fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", n), xmlHeader + `<Relationships xmlns="` + pkgRelNS + `">` + rels + `</Relationships>`})
		overrides = append(overrides, [2]string{fmt.Sprintf("/ppt/slides/slide%d.xml", n), pml + "slide+xml"})
		fmt.Fprintf(&sldIDs, `<p:sldId id="%d" r:id="rId%d"/>`, 255+n, n+2)
		fmt.Fprintf(&presRels, `<Relationship Id="rId%d" Type="%s/slide" Target="slides/slide%d.xml"/>`, n+2, relNS, n)
	}
	notesMasterRef := ""
	if notesCount > 0 {
		rid := len(slides) + 3
		fmt.Fprintf(&presRels, `<Relationship Id="rId%d" Type="%s/notesMaster" Target="notesMasters/notesMaster1.xml"/>`, rid, relNS)
		notesMasterRef = fmt.Sprintf(`<p:notesMasterIdLst><p:notesMasterId r:id="rId%d"/></p:notesMasterIdLst>`, rid)
		parts = append(parts,
			[2]string{"ppt/notesMasters/notesMaster1.xml", xmlHeader + `<p:notesMaster ` + pptxNSes + `><p:cSld><p:spTree>` + pptGroupHead + `</p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/></p:notesMaster>`},
			[2]string{"ppt/notesMasters/_rels/notesMaster1.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/theme" Target="../theme/theme2.xml"/></Relationships>`},
			[2]string{"ppt/theme/theme2.xml", pptxTheme()})
		overrides = append(overrides, [2]string{"/ppt/notesMasters/notesMaster1.xml", pml + "notesMaster+xml"}, [2]string{"/ppt/theme/theme2.xml", "application/vnd.openxmlformats-officedocument.theme+xml"})
	}
	pres := xmlHeader + `<p:presentation ` + pptxNSes + `><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>` + notesMasterRef + `<p:sldIdLst>` + sldIDs.String() + `</p:sldIdLst>` + fmt.Sprintf(`<p:sldSz cx="%d" cy="%d"/><p:notesSz cx="6858000" cy="9144000"/>`, pptSlideW, pptSlideH) + `</p:presentation>`
	all := [][2]string{
		{contentTypesPart, contentTypes(overrides...)},
		{"_rels/.rels", pkgRels("officeDocument", "ppt/presentation.xml")},
		{"docProps/core.xml", xmlHeader + fmt.Sprintf(coreProps, escText(spec.Title))},
		{"ppt/presentation.xml", pres},
		{"ppt/_rels/presentation.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `">` + presRels.String() + `</Relationships>`},
		{"ppt/slideMasters/slideMaster1.xml", master},
		{"ppt/slideMasters/_rels/slideMaster1.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `">` + masterRels.String() + `</Relationships>`},
		{"ppt/theme/theme1.xml", pptxTheme()},
	}
	return buildZip(append(all, parts...))
}

func slideShape(id int, name, phAttrs, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph%s/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>`, id, name, phAttrs)
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			b.WriteString(`<a:p><a:endParaRPr lang="en-US"/></a:p>`)
			continue
		}
		fmt.Fprintf(&b, `<a:p><a:r><a:rPr lang="en-US"/><a:t>%s</a:t></a:r></a:p>`, escText(line))
	}
	b.WriteString(`</p:txBody></p:sp>`)
	return b.String()
}

func notesSlideXML(text string) string {
	return xmlHeader + `<p:notes ` + pptxNSes + `><p:cSld><p:spTree>` + pptGroupHead + slideShape(2, "Notes 1", ` type="body" idx="1"`, text) + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:notes>`
}

// RetargetTemplate makes a package copied from a template (.dotx, .xltx, .potx) a plain document, by changing the main
// part's declared content type. A package that is not a template is returned as it is.
func RetargetTemplate(name string, data []byte) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".docx", ".xlsx", ".pptx":
	default:
		return data, nil
	}
	pkg, err := OpenPackage(data)
	if err != nil {
		return nil, err
	}
	ct, err := pkg.Read(contentTypesPart)
	if err != nil {
		return nil, err
	}
	updated := bytes.ReplaceAll(ct, []byte("spreadsheetml.template.main+xml"), []byte("spreadsheetml.sheet.main+xml"))
	updated = bytes.ReplaceAll(updated, []byte("wordprocessingml.template.main+xml"), []byte("wordprocessingml.document.main+xml"))
	updated = bytes.ReplaceAll(updated, []byte("presentationml.template.main+xml"), []byte("presentationml.presentation.main+xml"))
	if bytes.Equal(updated, ct) {
		return data, nil
	}
	c := NewChanges()
	c.Set[contentTypesPart] = updated
	return pkg.Write(c)
}

// --- PDF ---

// A new PDF is plain text pages in the built-in Helvetica. Those fonts cover Latin-1 only; anything else is refused, and
// the skill points to a Word document exported through the engine instead.

const (
	pdfPageW, pdfPageH = 612.0, 792.0
	pdfMargin          = 72.0
)

type pdfLine struct {
	text string
	size float64
	bold bool
	gap  float64
	x    float64
}

func createPDF(spec CreateSpec) ([]byte, error) {
	var lines []pdfLine
	add := func(text string, size float64, bold bool, gap, indent float64) error {
		for _, r := range text {
			if r > 0xFF {
				return refuse("the character %q needs a font this simple PDF writer does not have (it holds Latin-1 only). Create a .docx and render it through the engine instead", string(r))
			}
		}
		width := pdfPageW - 2*pdfMargin - indent
		first := true
		for _, hard := range strings.Split(text, "\n") {
			for _, l := range wrapHelvetica(hard, size, width) {
				g := 0.0
				if first {
					g = gap
					first = false
				}
				lines = append(lines, pdfLine{l, size, bold, g, indent})
			}
		}
		return nil
	}
	if spec.Title != "" {
		if err := add(spec.Title, 24, true, 0, 0); err != nil {
			return nil, err
		}
	}
	for i, b := range spec.Blocks {
		var err error
		switch b.Type {
		case "heading":
			size := map[int]float64{1: 18, 2: 14, 3: 12}[max(min(b.Level, 3), 1)]
			err = add(b.Text, size, true, 14, 0)
		case "paragraph", "":
			err = add(b.Text, 11, false, 8, 0)
		case "bullet":
			err = add("\u0095 "+b.Text, 11, false, 4, 12)
		case "numbered":
			err = add(b.Text, 11, false, 4, 12)
		case "page_break":
			lines = append(lines, pdfLine{gap: -1})
		case "table":
			for _, r := range b.Rows {
				if err = add(strings.Join(r, "  |  "), 10, false, 3, 0); err != nil {
					break
				}
			}
		default:
			err = refuse("block %d: unknown type %q; use heading, paragraph, bullet, numbered, table or page_break", i+1, b.Type)
		}
		if err != nil {
			return nil, err
		}
	}
	var pages []string
	var cur strings.Builder
	y := pdfPageH - pdfMargin
	flush := func() {
		pages = append(pages, cur.String())
		cur.Reset()
		y = pdfPageH - pdfMargin
	}
	for _, l := range lines {
		if l.gap < 0 {
			flush()
			continue
		}
		y -= l.gap + l.size*1.25
		if y < pdfMargin {
			flush()
			y -= l.size * 1.25
		}
		font := "F1"
		if l.bold {
			font = "F2"
		}
		fmt.Fprintf(&cur, "BT /%s %.1f Tf %.1f %.1f Td %s Tj ET\n", font, l.size, pdfMargin+l.x, y, pdfLiteral(l.text))
	}
	flush()
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := []int{}
	obj := func(body string) int {
		offsets = append(offsets, out.Len())
		n := len(offsets)
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, body)
		return n
	}
	// Objects 1-4 are fixed; pages follow.
	first := 5
	var kids []string
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", first+2*i))
	}
	obj("<</Type/Catalog/Pages 2 0 R>>")
	obj(fmt.Sprintf("<</Type/Pages/Count %d/Kids[%s]/MediaBox[0 0 %.0f %.0f]/Resources<</Font<</F1 3 0 R/F2 4 0 R>>>>>>", len(pages), strings.Join(kids, " "), pdfPageW, pdfPageH))
	obj("<</Type/Font/Subtype/Type1/BaseFont/Helvetica/Encoding/WinAnsiEncoding>>")
	obj("<</Type/Font/Subtype/Type1/BaseFont/Helvetica-Bold/Encoding/WinAnsiEncoding>>")
	for i, c := range pages {
		obj(fmt.Sprintf("<</Type/Page/Parent 2 0 R/Contents %d 0 R>>", first+2*i+1))
		obj(fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", len(c), c))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes(), nil
}

// pdfLiteral writes a Latin-1 string literal in a content stream.
func pdfLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('(')
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0x7E:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(')')
	return b.String()
}

// wrapHelvetica breaks at spaces with an average width: this writer has no font files, and an estimate only moves a
// line break.
func wrapHelvetica(text string, size, width float64) []string {
	maxChars := int(width / (size * 0.5))
	if len(text) <= maxChars {
		return []string{text}
	}
	var out []string
	cur := ""
	for _, w := range strings.Fields(text) {
		if cur != "" && len(cur)+1+len(w) > maxChars {
			out = append(out, cur)
			cur = ""
		}
		if cur != "" {
			cur += " "
		}
		cur += w
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
