package documents

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// XMLDoc is a parsed part: a tree of elements that knows where each one is in the part's bytes, so an edit can splice
// exactly one element and leave every other byte alone. Text content is kept decoded on the element it belongs to.
type XMLDoc struct {
	Data []byte
	Root *Node
	// Count is the number of elements.
	Count int
}

// Node is one element. Start and End are the offsets of the whole element; InnerStart and InnerEnd of what is between
// its tags (equal, at End, for a self-closed element).
type Node struct {
	Prefix, Local, NS    string
	Attrs                []xml.Attr
	Start, End           int
	InnerStart, InnerEnd int
	Parent               *Node
	Kids                 []*Node
	// Text is the character data directly inside it, decoded.
	Text string
	// SelfClosed: written as <a/>.
	SelfClosed bool
}

// Attr returns the value of the attribute with this local name (any namespace).
func (n *Node) Attr(local string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// HasAttr reports whether the attribute is there.
func (n *Node) HasAttr(local string) bool {
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return true
		}
	}
	return false
}

// Kid returns the first child with this local name, or nil.
func (n *Node) Kid(local string) *Node {
	if n == nil {
		return nil
	}
	for _, k := range n.Kids {
		if k.Local == local {
			return k
		}
	}
	return nil
}

// KidsNamed returns the children with this local name.
func (n *Node) KidsNamed(local string) []*Node {
	if n == nil {
		return nil
	}
	var out []*Node
	for _, k := range n.Kids {
		if k.Local == local {
			out = append(out, k)
		}
	}
	return out
}

// Walk visits n and everything below it; fn returning false skips what is below that node.
func (n *Node) Walk(fn func(*Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, k := range n.Kids {
		k.Walk(fn)
	}
}

// Find returns the elements below n (n included) with this local name.
func (n *Node) Find(local string) []*Node {
	var out []*Node
	n.Walk(func(c *Node) bool {
		if c.Local == local {
			out = append(out, c)
		}
		return true
	})
	return out
}

// Raw returns the element's bytes.
func (n *Node) Raw(d []byte) []byte { return d[n.Start:n.End] }

// Tag renders a tag name with this element's prefix.
func (n *Node) Tag(local string) string {
	if n.Prefix == "" {
		return local
	}
	return n.Prefix + ":" + local
}

// HasAncestor reports whether an ancestor has this local name.
func (n *Node) HasAncestor(local string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Local == local {
			return true
		}
	}
	return false
}

// ErrDoctype refuses document type declarations: an OOXML part never has one, and entities are how XML bombs work.
var ErrDoctype = errors.New("the XML has a DOCTYPE declaration, which is refused")

// ParseXML parses bytes into a tree with offsets. It keeps prefixes as written and resolves namespaces itself.
func ParseXML(data []byte) (*XMLDoc, error) {
	if len(data) > MaxXMLBytes {
		return nil, fmt.Errorf("the XML is %d MB, over the limit of %d MB", len(data)>>20, MaxXMLBytes>>20)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.CharsetReader = func(charset string, in io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "utf-8", "utf8", "us-ascii", "ascii":
			return in, nil
		}
		return nil, fmt.Errorf("unsupported XML encoding %q", charset)
	}
	doc := &XMLDoc{Data: data}
	var cur *Node
	depth := 0
	for {
		before := int(dec.InputOffset())
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		after := int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			doc.Count++
			if doc.Count > MaxXMLNodes {
				return nil, fmt.Errorf("the XML has more than %d elements", MaxXMLNodes)
			}
			depth++
			if depth > MaxXMLDepth {
				return nil, fmt.Errorf("the XML nests deeper than %d elements", MaxXMLDepth)
			}
			n := &Node{Prefix: t.Name.Space, Local: t.Name.Local, Attrs: copyAttrs(t.Attr), Start: before, InnerStart: after, InnerEnd: after, End: after, Parent: cur, SelfClosed: data[after-2] == '/'}
			n.NS = n.resolve(n.Prefix)
			if cur != nil {
				cur.Kids = append(cur.Kids, n)
			} else {
				if doc.Root != nil {
					return nil, errors.New("more than one root element")
				}
				doc.Root = n
			}
			cur = n
		case xml.EndElement:
			if cur == nil || t.Name.Local != cur.Local || t.Name.Space != cur.Prefix {
				return nil, fmt.Errorf("unexpected closing tag </%s>", t.Name.Local)
			}
			depth--
			cur.End = after
			if !cur.SelfClosed {
				cur.InnerEnd = before
			}
			cur = cur.Parent
		case xml.CharData:
			if cur != nil {
				cur.Text += string(t)
			} else if len(bytes.TrimSpace(bytes.TrimPrefix(t, []byte("\xEF\xBB\xBF")))) > 0 {
				return nil, errors.New("text outside the root element")
			}
		case xml.Directive:
			if bytes.HasPrefix(bytes.TrimSpace(t), []byte("DOCTYPE")) {
				return nil, ErrDoctype
			}
		}
	}
	if cur != nil {
		return nil, fmt.Errorf("unclosed element <%s>", cur.Local)
	}
	if doc.Root == nil {
		return nil, errors.New("no root element")
	}
	return doc, nil
}

func copyAttrs(in []xml.Attr) []xml.Attr {
	if len(in) == 0 {
		return nil
	}
	return append([]xml.Attr(nil), in...)
}

func (n *Node) resolve(prefix string) string {
	if prefix == "xml" {
		return "http://www.w3.org/XML/1998/namespace"
	}
	for c := n; c != nil; c = c.Parent {
		for _, a := range c.Attrs {
			if prefix == "" && a.Name.Space == "" && a.Name.Local == "xmlns" {
				return a.Value
			}
			if prefix != "" && a.Name.Space == "xmlns" && a.Name.Local == prefix {
				return a.Value
			}
		}
	}
	return ""
}

// Span is a replacement of bytes [Start, End) with Text.
type Span struct {
	Start, End int
	Text       string
}

// Splice applies spans, which must not overlap, to data.
func Splice(data []byte, spans []Span) []byte {
	if len(spans) == 0 {
		return data
	}
	sorted := append([]Span(nil), spans...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	var out bytes.Buffer
	out.Grow(len(data))
	at := 0
	for _, s := range sorted {
		out.Write(data[at:s.Start])
		out.WriteString(s.Text)
		at = s.End
	}
	out.Write(data[at:])
	return out.Bytes()
}

// spansOverlap reports whether any two spans overlap; an insertion may touch a neighbour.
func spansOverlap(spans []Span) bool {
	sorted := append([]Span(nil), spans...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Start < sorted[i-1].End {
			return true
		}
	}
	return false
}

// InsertBeforeClose adds text as the last content of n; a self-closed element gets a closing tag.
func InsertBeforeClose(data []byte, n *Node, text string) []byte {
	return Splice(data, []Span{insertKidSpan(data, n, text)})
}

func insertKidSpan(data []byte, n *Node, text string) Span {
	if !n.SelfClosed {
		return Span{n.InnerEnd, n.InnerEnd, text}
	}
	open := string(data[n.Start : n.End-2])
	return Span{n.Start, n.End, strings.TrimRight(open, " \t\r\n") + ">" + text + "</" + n.Tag(n.Local) + ">"}
}

// escText escapes character data; escAttr attribute values.
func escText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#13;").Replace(xmlSafe(s))
}

func escAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", "&#10;", "\t", "&#9;", "\r", "&#13;").Replace(xmlSafe(s))
}

// xmlSafe drops what XML 1.0 cannot hold at all.
func xmlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t', r == '\n', r == '\r':
			return r
		case r < 0x20, r == 0xFFFE, r == 0xFFFF, r >= 0xD800 && r <= 0xDFFF:
			return -1
		}
		return r
	}, s)
}

func startTag(data []byte, n *Node) []byte {
	if n.SelfClosed {
		return data[n.Start:n.End]
	}
	return data[n.Start:n.InnerStart]
}

// setAttrSpan returns the span that sets attr on n's start tag: replaces the value, or adds the attribute.
func setAttrSpan(data []byte, n *Node, attr, value string) Span {
	tag := startTag(data, n)
	if at := findAttrValue(tag, attr); at[0] >= 0 {
		return Span{n.Start + at[0], n.Start + at[1], escAttr(value)}
	}
	cut := len(tag) - 1
	if n.SelfClosed {
		cut = len(tag) - 2
	}
	for cut > 0 && (tag[cut-1] == ' ' || tag[cut-1] == '\n' || tag[cut-1] == '\t' || tag[cut-1] == '\r') {
		cut--
	}
	return Span{n.Start + cut, n.Start + cut, fmt.Sprintf(` %s="%s"`, attr, escAttr(value))}
}

// removeAttrSpan returns the span that removes attr from n's start tag, ok false when absent.
func removeAttrSpan(data []byte, n *Node, attr string) (Span, bool) {
	tag := startTag(data, n)
	at := findAttrValue(tag, attr)
	if at[0] < 0 {
		return Span{}, false
	}
	start := at[2]
	for start > 0 && (tag[start-1] == ' ' || tag[start-1] == '\t' || tag[start-1] == '\n' || tag[start-1] == '\r') {
		start--
	}
	return Span{n.Start + start, n.Start + at[1] + 1, ""}, true
}

// findAttrValue locates an attribute in a start tag: value start, value end, name start; -1 when absent. Names match
// whole, with a prefix if written with one.
func findAttrValue(tag []byte, attr string) [3]int {
	none := [3]int{-1, -1, -1}
	i := bytes.IndexAny(tag, " \t\r\n")
	if i < 0 {
		return none
	}
	for i < len(tag) {
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\r' || tag[i] == '\n') {
			i++
		}
		nameStart := i
		for i < len(tag) && tag[i] != '=' && tag[i] != ' ' && tag[i] != '/' && tag[i] != '>' {
			i++
		}
		name := string(tag[nameStart:i])
		for i < len(tag) && (tag[i] == '=' || tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\r' || tag[i] == '\n') {
			i++
		}
		if i >= len(tag) || (tag[i] != '"' && tag[i] != '\'') {
			return none
		}
		q := tag[i]
		end := bytes.IndexByte(tag[i+1:], q)
		if end < 0 {
			return none
		}
		if name == attr {
			return [3]int{i + 1, i + 1 + end, nameStart}
		}
		i = i + 1 + end + 1
	}
	return none
}
