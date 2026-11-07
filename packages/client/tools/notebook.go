package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// notebookOutputChars bounds one cell output in a Read result (head and tail kept).
const notebookOutputChars = 8 * 1024

// A notebook is edited by splicing its bytes: only the cell that changes is rewritten, in the layout of the file, so the
// rest of the JSON stays byte for byte what it was (key order, indentation, line ends, the last newline).

// nbDoc locates the cells of a notebook in its bytes.
type nbDoc struct {
	data []byte
	// arrOpen and arrClose are the offsets of the cells array's brackets.
	arrOpen, arrClose int
	cells             []nbCell
	major, minor      int
	// topIndent is the whitespace before the first key of the document.
	topIndent string
}

type nbCell struct {
	start, end int
	id         string
	cellType   string
	source     string
}

func parseNotebook(data []byte) (*nbDoc, error) {
	doc := &nbDoc{data: data, arrOpen: -1}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	if i, open := bytes.IndexByte(data, '"'), bytes.IndexByte(data, '{'); i > open {
		doc.topIndent = string(data[open+1 : i])
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		if key == "cells" {
			if err := doc.readCells(dec); err != nil {
				return nil, err
			}
			continue
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		switch key {
		case "nbformat":
			doc.major, _ = strconv.Atoi(string(raw))
		case "nbformat_minor":
			doc.minor, _ = strconv.Atoi(string(raw))
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if doc.arrOpen < 0 {
		return nil, errors.New(`no "cells" array (only nbformat 4 notebooks are supported)`)
	}
	return doc, nil
}

func (d *nbDoc) readCells(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('[') {
		return errors.New(`"cells" is not an array`)
	}
	d.arrOpen = int(dec.InputOffset()) - 1
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		end := int(dec.InputOffset())
		var c struct {
			ID       string          `json:"id"`
			CellType string          `json:"cell_type"`
			Source   json.RawMessage `json:"source"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return fmt.Errorf("cell %d: %v", len(d.cells), err)
		}
		d.cells = append(d.cells, nbCell{start: end - len(raw), end: end, id: c.ID, cellType: c.CellType, source: nbText(c.Source)})
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	d.arrClose = int(dec.InputOffset()) - 1
	return nil
}

// nbText reads a multiline field of nbformat: a string, or an array of lines.
func nbText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var lines []string
	if json.Unmarshal(raw, &lines) == nil {
		return strings.Join(lines, "")
	}
	return ""
}

// hasIDs reports whether cells of this notebook carry ids (nbformat 4.5 and later require them).
func (d *nbDoc) hasIDs() bool {
	for _, c := range d.cells {
		if c.id != "" {
			return true
		}
	}
	return d.major > 4 || (d.major == 4 && d.minor >= 5)
}

// find returns the index of the addressed cell.
func (d *nbDoc) find(id string, index *int) (int, error) {
	if id != "" {
		var known []string
		for i, c := range d.cells {
			if c.id == id {
				return i, nil
			}
			if c.id != "" && len(known) < 20 {
				known = append(known, c.id)
			}
		}
		return 0, fmt.Errorf("no cell with id %q. Ids in this notebook: %s", id, strings.Join(known, ", "))
	}
	if *index < 0 || *index >= len(d.cells) {
		return 0, fmt.Errorf("cell_index %d is out of range: the notebook has %d cells", *index, len(d.cells))
	}
	return *index, nil
}

// nbMember is one key of a cell object, with its raw value.
type nbMember struct {
	key   string
	value []byte
}

// nbObject is a parsed cell object and the whitespace it is written with.
type nbObject struct {
	members []nbMember
	// open: between { and the first key. colon: between key and value. comma: between a value and the next key.
	// closing: between the last value and }.
	open, colon, comma, closing string
}

func parseObject(raw []byte) (*nbObject, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	o := &nbObject{}
	last := int(dec.InputOffset())
	for dec.More() {
		next := last + bytes.IndexByte(raw[last:], '"')
		if len(o.members) == 0 {
			o.open = string(raw[last:next])
		} else if o.comma == "" {
			o.comma = string(raw[last:next])
		}
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		keyEnd := int(dec.InputOffset())
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		last = int(dec.InputOffset())
		if len(o.members) == 0 {
			o.colon = string(raw[keyEnd : last-len(val)])
		}
		o.members = append(o.members, nbMember{key, append([]byte(nil), val...)})
	}
	o.closing = string(raw[last : len(raw)-1])
	if o.comma == "" {
		o.comma = "," + o.open
	}
	return o, nil
}

func (o *nbObject) get(key string) []byte {
	for _, m := range o.members {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

func (o *nbObject) set(key string, value []byte) {
	for i, m := range o.members {
		if m.key == key {
			o.members[i].value = value
			return
		}
	}
	o.members = append(o.members, nbMember{key, value})
}

func (o *nbObject) remove(key string) {
	for i, m := range o.members {
		if m.key == key {
			o.members = append(o.members[:i], o.members[i+1:]...)
			return
		}
	}
}

func (o *nbObject) render() []byte {
	var b bytes.Buffer
	b.WriteString("{" + o.open)
	for i, m := range o.members {
		if i > 0 {
			b.WriteString(o.comma)
		}
		b.Write(nbString(m.key))
		b.WriteString(o.colon)
		b.Write(m.value)
	}
	b.WriteString(o.closing + "}")
	return b.Bytes()
}

// nbString encodes a JSON string as nbformat writers do: without escaping <, > and &.
func nbString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// sourceValue encodes new source in the form of old (a string stays a string, otherwise an array of lines), using the
// layout of old when it has one and that of the surrounding object when not.
func sourceValue(text string, old []byte, o *nbObject) []byte {
	if len(old) > 0 && old[0] == '"' {
		return nbString(text)
	}
	open, closing := o.open+strings.TrimPrefix(o.open, o.closing), o.open
	if lay, ok := arrayLayout(old); ok {
		open, closing = lay[0], lay[1]
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return []byte("[]")
	}
	var b bytes.Buffer
	b.WriteString("[")
	for i, l := range lines {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(open)
		b.Write(nbString(l))
	}
	b.WriteString(closing + "]")
	return b.Bytes()
}

// arrayLayout returns the whitespace after [ and before ] of a non-empty array.
func arrayLayout(raw []byte) ([2]string, bool) {
	if len(raw) < 2 || raw[0] != '[' {
		return [2]string{}, false
	}
	const space = " \t\r\n"
	i := 1
	for i < len(raw) && strings.IndexByte(space, raw[i]) >= 0 {
		i++
	}
	if i >= len(raw)-1 {
		return [2]string{}, false // empty
	}
	j := len(raw) - 1
	for strings.IndexByte(space, raw[j-1]) >= 0 {
		j--
	}
	return [2]string{string(raw[1:i]), string(raw[j : len(raw)-1])}, true
}

// splice returns the document with [from, to) replaced.
func (d *nbDoc) splice(from, to int, with []byte) []byte {
	out := make([]byte, 0, len(d.data)+len(with))
	out = append(out, d.data[:from]...)
	out = append(out, with...)
	return append(out, d.data[to:]...)
}

// newCell renders an empty cell in the layout of the notebook, with the keys in the order nbformat writers use.
func (d *nbDoc) newCell(cellType, source string) ([]byte, error) {
	o := &nbObject{colon: ": "}
	if len(d.cells) > 0 {
		like, err := parseObject(d.data[d.cells[0].start:d.cells[0].end])
		if err != nil {
			return nil, err
		}
		o.open, o.colon, o.comma, o.closing = like.open, like.colon, like.comma, like.closing
	} else {
		// No cell to copy from: nested under the document's own indentation.
		unit := strings.TrimLeft(d.topIndent, "\r\n")
		o.open, o.closing = d.topIndent+unit+unit, d.topIndent+unit
		if d.topIndent == "" {
			o.colon = ":"
		}
		o.comma = "," + o.open
	}
	o.set("cell_type", nbString(cellType))
	if cellType == "code" {
		o.set("execution_count", []byte("null"))
	}
	if d.hasIDs() {
		o.set("id", nbString(d.newID()))
	}
	o.set("metadata", []byte("{}"))
	if cellType == "code" {
		o.set("outputs", []byte("[]"))
	}
	o.set("source", sourceValue(source, nil, o))
	return o.render(), nil
}

func (d *nbDoc) newID() string {
	for {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		taken := false
		for _, c := range d.cells {
			taken = taken || c.id == id
		}
		if !taken {
			return id
		}
	}
}

// insert adds a rendered cell after index at (-1: first).
func (d *nbDoc) insert(at int, cell []byte) []byte {
	if len(d.cells) == 0 {
		unit := strings.TrimLeft(d.topIndent, "\r\n")
		return d.splice(d.arrOpen+1, d.arrClose, []byte(d.topIndent+unit+string(cell)+d.topIndent))
	}
	gap := string(d.data[d.arrOpen+1 : d.cells[0].start])
	if at < 0 {
		return d.splice(d.cells[0].start, d.cells[0].start, append(cell, []byte(","+gap)...))
	}
	return d.splice(d.cells[at].end, d.cells[at].end, append([]byte(","+gap), cell...))
}

// remove deletes a cell and one separator.
func (d *nbDoc) remove(at int) []byte {
	switch {
	case len(d.cells) == 1:
		return d.splice(d.arrOpen+1, d.arrClose, nil)
	case at == 0:
		return d.splice(d.cells[0].start, d.cells[1].start, nil)
	}
	return d.splice(d.cells[at-1].end, d.cells[at].end, nil)
}

// replace rewrites a cell: new source when source is set, the cell type when cellType is set. Outputs belong to the old
// source, so a code cell that was replaced loses them; only code cells have outputs, and only others have attachments.
func (d *nbDoc) replace(at int, source *string, cellType string) ([]byte, error) {
	c := d.cells[at]
	o, err := parseObject(d.data[c.start:c.end])
	if err != nil {
		return nil, err
	}
	if source != nil {
		o.set("source", sourceValue(*source, o.get("source"), o))
	}
	effective := c.cellType
	if cellType != "" {
		o.set("cell_type", nbString(cellType))
		effective = cellType
	}
	if effective == "code" {
		o.set("outputs", []byte("[]"))
		o.set("execution_count", []byte("null"))
		o.remove("attachments")
	} else {
		o.remove("outputs")
		o.remove("execution_count")
	}
	return d.splice(c.start, c.end, o.render()), nil
}

// NotebookEdit changes one cell of a Jupyter notebook.
type NotebookEdit struct{}

type notebookEditInput struct {
	NotebookPath string  `json:"notebook_path"`
	CellID       string  `json:"cell_id"`
	CellIndex    *int    `json:"cell_index"`
	NewSource    *string `json:"new_source"`
	CellType     string  `json:"cell_type"`
	EditMode     string  `json:"edit_mode"`
}

func (*NotebookEdit) Name() string                  { return "NotebookEdit" }
func (*NotebookEdit) ReadOnly(json.RawMessage) bool { return false }

func (in *notebookEditInput) validate() string {
	if in.NotebookPath == "" {
		return "InputValidationError: notebook_path is required"
	}
	if !filepath.IsAbs(in.NotebookPath) {
		return fmt.Sprintf("notebook_path must be an absolute path, got %q", in.NotebookPath)
	}
	if !strings.EqualFold(filepath.Ext(in.NotebookPath), ".ipynb") {
		return in.NotebookPath + " is not a notebook (.ipynb). Use Edit or Write for other files."
	}
	switch in.CellType {
	case "", "code", "markdown", "raw":
	default:
		return "InputValidationError: cell_type must be code, markdown or raw"
	}
	if in.CellID != "" && in.CellIndex != nil {
		return "InputValidationError: pass either cell_id or cell_index, not both"
	}
	addressed := in.CellID != "" || in.CellIndex != nil
	switch in.EditMode {
	case "replace":
		if !addressed {
			return "InputValidationError: replace needs cell_id or cell_index"
		}
		if in.NewSource == nil && in.CellType == "" {
			return "InputValidationError: replace needs new_source, cell_type or both"
		}
	case "insert":
		if in.NewSource == nil || in.CellType == "" {
			return "InputValidationError: insert needs new_source and cell_type"
		}
	case "delete":
		if !addressed {
			return "InputValidationError: delete needs cell_id or cell_index"
		}
	default:
		return "InputValidationError: edit_mode must be replace, insert or delete"
	}
	return ""
}

func (*NotebookEdit) Run(ctx context.Context, env *Env, call Call) Result {
	var in notebookEditInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.EditMode == "" {
		in.EditMode = "replace"
	}
	if msg := in.validate(); msg != "" {
		return ErrorResult("%s", msg)
	}
	path := filepath.Clean(in.NotebookPath)
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return ErrorResult("File does not exist. Current working directory: %s", env.Cwd)
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
	doc, err := parseNotebook(data)
	if err != nil {
		return ErrorResult("%s is not a valid notebook: %v", path, err)
	}
	at := -1
	if in.CellID != "" || in.CellIndex != nil {
		if at, err = doc.find(in.CellID, in.CellIndex); err != nil {
			return ErrorResult("%v", err)
		}
	}

	var updated []byte
	var oldSource, newSource, cellID, cellType string
	index := at
	switch in.EditMode {
	case "replace":
		c := doc.cells[at]
		oldSource, newSource, cellID, cellType = c.source, c.source, c.id, c.cellType
		if in.NewSource != nil {
			newSource = *in.NewSource
		}
		if in.CellType != "" {
			cellType = in.CellType
		}
		if newSource == c.source && cellType == c.cellType {
			return ErrorResult("No changes to make: the cell already has this source and type.")
		}
		updated, err = doc.replace(at, in.NewSource, in.CellType)
	case "insert":
		var cell []byte
		newSource, cellType, index = *in.NewSource, in.CellType, at+1
		if cell, err = doc.newCell(cellType, newSource); err == nil {
			updated = doc.insert(at, cell)
			var v struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(cell, &v)
			cellID = v.ID
		}
	case "delete":
		c := doc.cells[at]
		oldSource, cellID, cellType = c.source, c.id, c.cellType
		updated = doc.remove(at)
	}
	if err != nil {
		return ErrorResult("%s: %v", path, err)
	}
	if !json.Valid(updated) {
		return ErrorResult("%s: the edit would not leave valid JSON; the file was not changed.", path)
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	env.beforeEdit(path)
	if err := os.WriteFile(path, updated, st.Mode().Perm()); err != nil {
		return ErrorResult("%v", err)
	}
	env.Files.MarkReadContent(path, updated)
	env.afterEdit(ctx, path)

	name := fmt.Sprintf("cell %d", index)
	if cellID != "" {
		name = fmt.Sprintf("cell %s (index %d)", cellID, index)
	}
	verb := map[string]string{"replace": "Replaced", "insert": "Inserted", "delete": "Deleted"}[in.EditMode]
	msg := fmt.Sprintf("%s %s in %s.", verb, name, path)
	if in.EditMode == "replace" && cellType == "code" {
		msg += " Its outputs and execution count were cleared."
	}
	// The diff of the cell's source: the JSON around it says nothing to a reader.
	patch := DiffLines(oldSource, newSource, 3)
	r := TextResult(msg, map[string]any{"filePath": path, "editMode": in.EditMode, "cellId": cellID, "cellIndex": index, "cellType": cellType, "structuredPatch": nonNilHunks(patch)})
	add, del := DiffStats(patch)
	r.Summary = fmt.Sprintf("%s %s (+%d -%d)", verb, name, add, del)
	return r
}

// readNotebook renders a notebook for the model: every cell with its id, index, type and outputs. offset and limit count
// cells. ok is false when data is not a notebook.
func readNotebook(path string, data []byte, in readInput) (Result, bool) {
	var nb struct {
		Cells         *[]nbReadCell `json:"cells"`
		NBFormat      int           `json:"nbformat"`
		NBFormatMinor int           `json:"nbformat_minor"`
		Metadata      struct {
			Kernelspec struct {
				Language string `json:"language"`
				Name     string `json:"name"`
			} `json:"kernelspec"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &nb); err != nil || nb.Cells == nil {
		return Result{}, false
	}
	cells := *nb.Cells
	first := 0
	if in.Offset != nil && *in.Offset > 0 {
		first = *in.Offset - 1
	}
	limit := len(cells)
	if in.Limit != nil && *in.Limit > 0 {
		limit = *in.Limit
	}
	var b strings.Builder
	kernel := nb.Metadata.Kernelspec.Language
	if kernel == "" {
		kernel = nb.Metadata.Kernelspec.Name
	}
	fmt.Fprintf(&b, "Jupyter notebook, nbformat %d.%d, %d cells", nb.NBFormat, nb.NBFormatMinor, len(cells))
	if kernel != "" {
		b.WriteString(", kernel " + kernel)
	}
	b.WriteString(".\n")
	shown := 0
	for i := first; i < len(cells) && shown < limit; i++ {
		cells[i].render(&b, i)
		shown++
	}
	out := b.String()
	if len(out) > ReadMaxOutputBytes {
		return ErrorResult("Notebook content (%d KB) exceeds the maximum allowed size (%d KB). Use offset and limit, which count cells here, to read part of it.", len(out)/1024, ReadMaxOutputBytes/1024), true
	}
	r := TextResult(out, map[string]any{"type": "notebook", "file": map[string]any{"filePath": path, "numCells": shown, "startCell": first + 1, "totalCells": len(cells)}})
	r.Summary = fmt.Sprintf("Read %d cells", shown)
	return r, true
}

type nbReadCell struct {
	ID             string          `json:"id"`
	CellType       string          `json:"cell_type"`
	Source         json.RawMessage `json:"source"`
	ExecutionCount *int            `json:"execution_count"`
	Outputs        []struct {
		OutputType string                     `json:"output_type"`
		Name       string                     `json:"name"`
		Text       json.RawMessage            `json:"text"`
		Data       map[string]json.RawMessage `json:"data"`
		EName      string                     `json:"ename"`
		EValue     string                     `json:"evalue"`
		Traceback  []string                   `json:"traceback"`
	} `json:"outputs"`
}

func (c nbReadCell) render(b *strings.Builder, index int) {
	fmt.Fprintf(b, "<cell index=\"%d\"", index)
	if c.ID != "" {
		fmt.Fprintf(b, " id=%q", c.ID)
	}
	fmt.Fprintf(b, " type=%q", c.CellType)
	if c.ExecutionCount != nil {
		fmt.Fprintf(b, " execution_count=\"%d\"", *c.ExecutionCount)
	}
	b.WriteString(">\n")
	if src := strings.TrimRight(nbText(c.Source), "\n"); src != "" {
		b.WriteString(src + "\n")
	}
	for _, o := range c.Outputs {
		fmt.Fprintf(b, "<output type=%q", o.OutputType)
		if o.Name != "" {
			fmt.Fprintf(b, " name=%q", o.Name)
		}
		b.WriteString(">\n")
		var text string
		switch o.OutputType {
		case "stream":
			text = nbText(o.Text)
		case "error":
			text = o.EName + ": " + o.EValue
			if len(o.Traceback) > 0 {
				text = strings.Join(o.Traceback, "\n")
			}
		default:
			text = nbOutputData(o.Data)
		}
		text = strings.TrimRight(StripTerminalCodes(text), "\n")
		if text != "" {
			b.WriteString(headTail(text, notebookOutputChars) + "\n")
		}
		b.WriteString("</output>\n")
	}
	b.WriteString("</cell>\n")
}

// nbOutputData picks what to show of a rich output: its plain text (else the first other text form), and a line for
// each image, which is not sent.
func nbOutputData(data map[string]json.RawMessage) string {
	var parts []string
	if raw, ok := data["text/plain"]; ok {
		parts = append(parts, nbText(raw))
	} else {
		for _, mime := range []string{"text/markdown", "text/latex", "text/html", "application/json"} {
			if raw, ok := data[mime]; ok {
				text := nbText(raw)
				if text == "" {
					text = string(raw)
				}
				parts = append(parts, text)
				break
			}
		}
	}
	var images []string
	for mime := range data {
		if strings.HasPrefix(mime, "image/") {
			images = append(images, mime)
		}
	}
	sort.Strings(images)
	for _, mime := range images {
		parts = append(parts, fmt.Sprintf("[%s output, about %d KB, not shown]", mime, len(nbText(data[mime]))*3/4/1024))
	}
	return strings.Join(parts, "\n")
}
