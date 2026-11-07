package documents

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// Package is an OOXML package opened from bytes. Parts are read on demand and bounded on the real bytes, not on what
// the zip headers declare.
type Package struct {
	data    []byte
	entries []*zip.File
	byName  map[string]*zip.File
	cache   map[string][]byte
	// dupes are names present more than once.
	dupes []string
}

// OpenPackage reads the zip directory, checking the limits that do not need the content.
func OpenPackage(data []byte) (*Package, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a readable zip package: %v", err)
	}
	if len(r.File) > MaxEntries {
		return nil, fmt.Errorf("the package has %d entries, over the limit of %d", len(r.File), MaxEntries)
	}
	p := &Package{data: data, entries: r.File, byName: make(map[string]*zip.File, len(r.File)), cache: map[string][]byte{}}
	var total uint64
	for _, f := range r.File {
		if badPartName(f.Name) {
			return nil, fmt.Errorf("the package holds an entry with an unsafe name: %q", f.Name)
		}
		if _, dup := p.byName[f.Name]; dup {
			p.dupes = append(p.dupes, f.Name)
			continue
		}
		p.byName[f.Name] = f
		total += f.UncompressedSize64
		if err := checkBomb(f.Name, f.UncompressedSize64, f.CompressedSize64); err != nil {
			return nil, err
		}
	}
	if total > MaxTotalBytes {
		return nil, fmt.Errorf("the package expands to %d MB, over the limit of %d MB", total>>20, uint64(MaxTotalBytes)>>20)
	}
	return p, nil
}

func checkBomb(name string, size, compressed uint64) error {
	if size > MaxPartBytes {
		return fmt.Errorf("the part %s expands to %d MB, over the limit of %d MB", name, size>>20, MaxPartBytes>>20)
	}
	if size > MinRatioBytes && compressed > 0 && size/compressed > MaxRatio {
		return fmt.Errorf("the part %s compresses %d to 1, which looks like a zip bomb", name, size/compressed)
	}
	return nil
}

func badPartName(n string) bool {
	if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, "\\") || strings.ContainsRune(n, 0) {
		return true
	}
	for _, s := range strings.Split(n, "/") {
		if s == ".." {
			return true
		}
	}
	return false
}

// Names lists the parts in package order.
func (p *Package) Names() []string {
	out := make([]string, 0, len(p.entries))
	seen := map[string]bool{}
	for _, f := range p.entries {
		if !seen[f.Name] {
			seen[f.Name] = true
			out = append(out, f.Name)
		}
	}
	return out
}

// Has reports whether the part exists.
func (p *Package) Has(name string) bool {
	_, ok := p.byName[name]
	return ok
}

// Read returns a part's bytes. The headers may lie, so the limit applies to what comes out.
func (p *Package) Read(name string) ([]byte, error) {
	if b, ok := p.cache[name]; ok {
		return b, nil
	}
	f, ok := p.byName[name]
	if !ok {
		return nil, fmt.Errorf("the package has no part %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, MaxPartBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	if len(b) > MaxPartBytes {
		return nil, fmt.Errorf("the part %s expands past the limit of %d MB", name, MaxPartBytes>>20)
	}
	if err := checkBomb(name, uint64(len(b)), f.CompressedSize64); err != nil {
		return nil, err
	}
	p.cache[name] = b
	return b, nil
}

// XML parses a part. The offsets of the document are into that part's bytes.
func (p *Package) XML(name string) (*XMLDoc, error) {
	b, err := p.Read(name)
	if err != nil {
		return nil, err
	}
	d, err := ParseXML(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return d, nil
}

// Changes describes a new version of a package. Everything not named keeps its compressed bytes.
type Changes struct {
	Set    map[string][]byte
	Remove map[string]bool
}

// NewChanges returns an empty change set.
func NewChanges() *Changes {
	return &Changes{Set: map[string][]byte{}, Remove: map[string]bool{}}
}

// Current returns a part's latest bytes in this change set, or what the package holds.
func (c *Changes) Current(p *Package, name string) ([]byte, error) {
	if b, ok := c.Set[name]; ok {
		return b, nil
	}
	return p.Read(name)
}

// Write writes the package with c applied: changed parts in their places, new parts at the end, the other entries
// copied byte for byte.
func (p *Package) Write(c *Changes) ([]byte, error) {
	var out bytes.Buffer
	out.Grow(len(p.data) + 1024)
	w := zip.NewWriter(&out)
	done := map[string]bool{}
	for _, f := range p.entries {
		if done[f.Name] {
			continue // a duplicate name is dropped: the first one is what we read
		}
		done[f.Name] = true
		if c.Remove[f.Name] {
			continue
		}
		if b, ok := c.Set[f.Name]; ok {
			if err := writeDeflated(w, f.FileHeader, b); err != nil {
				return nil, err
			}
			continue
		}
		if err := w.Copy(f); err != nil {
			return nil, fmt.Errorf("copying %s: %v", f.Name, err)
		}
	}
	var added []string
	for n := range c.Set {
		if !done[n] && !c.Remove[n] {
			added = append(added, n)
		}
	}
	sort.Strings(added)
	for _, n := range added {
		h := zip.FileHeader{Name: n, Method: zip.Deflate}
		if err := writeDeflated(w, h, c.Set[n]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeDeflated(w *zip.Writer, old zip.FileHeader, b []byte) error {
	h := zip.FileHeader{Name: old.Name, Method: old.Method, Modified: old.Modified, Comment: old.Comment, ExternalAttrs: old.ExternalAttrs, CreatorVersion: old.CreatorVersion}
	fw, err := w.CreateHeader(&h)
	if err != nil {
		return err
	}
	_, err = fw.Write(b)
	return err
}

// Relationship is one entry of a .rels part.
type Relationship struct {
	ID, Type, Target string
	External         bool
}

// RelsPart names the relationships part of a part ("" for the package's).
func RelsPart(part string) string {
	if part == "" {
		return "_rels/.rels"
	}
	return path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
}

// Rels reads the relationships of a part; none is not an error. Targets stay as written.
func (p *Package) Rels(part string) ([]Relationship, error) {
	name := RelsPart(part)
	if !p.Has(name) {
		return nil, nil
	}
	d, err := p.XML(name)
	if err != nil {
		return nil, err
	}
	return relsOf(d), nil
}

func relsOf(d *XMLDoc) []Relationship {
	var out []Relationship
	for _, n := range d.Root.Kids {
		if n.Local == "Relationship" {
			out = append(out, Relationship{ID: n.Attr("Id"), Type: n.Attr("Type"), Target: n.Attr("Target"), External: n.Attr("TargetMode") == "External"})
		}
	}
	return out
}

// ResolveTarget turns a relationship target into a part name.
func ResolveTarget(from, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return strings.TrimPrefix(path.Join(path.Dir(from), target), "/")
}

// relByType finds the first relationship whose type ends with suffix (transitional and strict differ in the prefix).
func relByType(rels []Relationship, suffix string) (Relationship, bool) {
	for _, r := range rels {
		if strings.HasSuffix(r.Type, suffix) {
			return r, true
		}
	}
	return Relationship{}, false
}

func relByID(rels []Relationship, id string) (Relationship, bool) {
	for _, r := range rels {
		if r.ID == id {
			return r, true
		}
	}
	return Relationship{}, false
}

// officeDocumentPart finds the main part from the package relationships.
func (p *Package) officeDocumentPart() (string, error) {
	rels, err := p.Rels("")
	if err != nil {
		return "", err
	}
	r, ok := relByType(rels, "/officeDocument")
	if !ok {
		return "", fmt.Errorf("the package has no main document relationship")
	}
	return ResolveTarget("", r.Target), nil
}

const contentTypesPart = "[Content_Types].xml"

// addContentType declares a new part's type in the change set.
func (c *Changes) addContentType(p *Package, part, contentType string) error {
	ct, err := c.Current(p, contentTypesPart)
	if err != nil {
		return err
	}
	d, err := ParseXML(ct)
	if err != nil {
		return err
	}
	s := fmt.Sprintf(`<Override PartName="/%s" ContentType="%s"/>`, escAttr(part), escAttr(contentType))
	c.Set[contentTypesPart] = InsertBeforeClose(ct, d.Root, s)
	return nil
}

// removeContentType removes a part's Override.
func (c *Changes) removeContentType(p *Package, part string) error {
	ct, err := c.Current(p, contentTypesPart)
	if err != nil {
		return err
	}
	d, err := ParseXML(ct)
	if err != nil {
		return err
	}
	var spans []Span
	for _, n := range d.Root.Kids {
		if n.Local == "Override" && strings.EqualFold(n.Attr("PartName"), "/"+part) {
			spans = append(spans, Span{n.Start, n.End, ""})
		}
	}
	c.Set[contentTypesPart] = Splice(ct, spans)
	return nil
}

// contentTypeOf finds a part's declared content type.
func contentTypeOf(ct *XMLDoc, part string) string {
	ext := strings.TrimPrefix(path.Ext(part), ".")
	def := ""
	for _, n := range ct.Root.Kids {
		switch n.Local {
		case "Override":
			if strings.EqualFold(n.Attr("PartName"), "/"+part) {
				return n.Attr("ContentType")
			}
		case "Default":
			if strings.EqualFold(n.Attr("Extension"), ext) {
				def = n.Attr("ContentType")
			}
		}
	}
	return def
}

// addRelationship adds a relationship to a part (creating its .rels part if needed) and returns the new id.
func (c *Changes) addRelationship(p *Package, part, typ, target string, external bool) (string, error) {
	name := RelsPart(part)
	var rels []byte
	if b, ok := c.Set[name]; ok {
		rels = b
	} else if p.Has(name) && !c.Remove[name] {
		b, err := p.Read(name)
		if err != nil {
			return "", err
		}
		rels = b
	}
	created := len(rels) == 0
	if created {
		rels = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`)
	}
	d, err := ParseXML(rels)
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, n := range d.Root.Kids {
		used[n.Attr("Id")] = true
	}
	id := ""
	for i := 1; ; i++ {
		id = fmt.Sprintf("rId%d", i)
		if !used[id] {
			break
		}
	}
	mode := ""
	if external {
		mode = ` TargetMode="External"`
	}
	s := fmt.Sprintf(`<Relationship Id="%s" Type="%s" Target="%s"%s/>`, id, escAttr(typ), escAttr(target), mode)
	c.Set[name] = InsertBeforeClose(rels, d.Root, s)
	return id, nil
}

// removeRelationship removes a relationship of a part by id.
func (c *Changes) removeRelationship(p *Package, part, id string) error {
	name := RelsPart(part)
	rels, err := c.Current(p, name)
	if err != nil {
		return err
	}
	d, err := ParseXML(rels)
	if err != nil {
		return err
	}
	var spans []Span
	for _, n := range d.Root.Kids {
		if n.Attr("Id") == id {
			spans = append(spans, Span{n.Start, n.End, ""})
		}
	}
	c.Set[name] = Splice(rels, spans)
	return nil
}

// checkPackage runs what every OOXML package must satisfy, and returns findings keyed by part.
func (p *Package) checkPackage() []Finding {
	var out []Finding
	bad := func(key, format string, args ...any) {
		out = append(out, Finding{SeverityError, "package:" + key, fmt.Sprintf(format, args...)})
	}
	for _, n := range p.dupes {
		bad("dup:"+n, "the entry %s appears more than once in the zip", n)
	}
	ctb, err := p.Read(contentTypesPart)
	if err != nil {
		bad("content-types", "%v", err)
		return out
	}
	ct, err := ParseXML(ctb)
	if err != nil {
		bad("content-types", "[Content_Types].xml is not well-formed: %v", err)
		return out
	}
	for _, n := range p.Names() {
		if strings.HasSuffix(n, "/") || n == contentTypesPart {
			continue
		}
		if contentTypeOf(ct, n) == "" {
			bad("no-type:"+n, "the part %s has no content type", n)
		}
		if isXMLPart(n) {
			if size := p.byName[n].UncompressedSize64; size > MaxXMLBytes {
				out = append(out, Finding{SeverityUnverified, "package:large:" + n, fmt.Sprintf("the part %s is too large to check (%d MB)", n, size>>20)})
				continue
			}
			if _, err := p.XML(n); err != nil {
				bad("xml:"+n, "%v", err)
			}
		}
	}
	for _, n := range ct.Root.Kids {
		if n.Local == "Override" {
			part := strings.TrimPrefix(n.Attr("PartName"), "/")
			if !p.Has(part) {
				bad("ghost:"+part, "[Content_Types].xml declares %s, which is not in the package", part)
			}
		}
	}
	// Relationship targets.
	for _, n := range p.Names() {
		if !strings.HasSuffix(n, ".rels") {
			continue
		}
		src := relsSource(n)
		rels, err := p.Rels(src)
		if err != nil {
			continue // reported as XML above
		}
		ids := map[string]bool{}
		for _, r := range rels {
			if ids[r.ID] {
				bad("rel-dup:"+n+":"+r.ID, "%s uses the relationship id %s twice", n, r.ID)
			}
			ids[r.ID] = true
			if r.External || strings.HasPrefix(r.Target, "#") {
				continue
			}
			if t := ResolveTarget(src, r.Target); !p.Has(t) {
				bad("rel-target:"+n+":"+r.Target, "%s points to %s, which is not in the package", n, t)
			}
		}
	}
	return out
}

func relsSource(relsName string) string {
	if relsName == "_rels/.rels" {
		return ""
	}
	dir, base := path.Split(relsName)
	return strings.TrimPrefix(path.Join(path.Dir(strings.TrimSuffix(dir, "/")), strings.TrimSuffix(base, ".rels")), "./")
}

func isXMLPart(n string) bool {
	return strings.HasSuffix(n, ".xml") || strings.HasSuffix(n, ".rels")
}
