package documents

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// Comment threads and resolved state. ECMA-376 comments are flat; Word 2013 and later keep threads and the done flag
// in the commentsExtended part ([MS-DOCX] 2.5.x, namespace w15): one w15:commentEx per comment, keyed by the
// w14:paraId of the comment's last paragraph, with paraIdParent naming the comment it answers and done="1" when the
// thread is resolved. Word 2016 adds commentsIds (durable ids) and commentsExtensible (UTC dates); when a file has those
// parts, new comments get entries there too, and files without them are left without them.

const (
	w14NS   = "http://schemas.microsoft.com/office/word/2010/wordml"
	w15NS   = "http://schemas.microsoft.com/office/word/2012/wordml"
	w16cid  = "http://schemas.microsoft.com/office/word/2016/wordml/cid"
	w16cex  = "http://schemas.microsoft.com/office/word/2018/wordml/cex"
	mcNS    = "http://schemas.openxmlformats.org/markup-compatibility/2006"
	exRel   = "http://schemas.microsoft.com/office/2011/relationships/commentsExtended"
	idsRel  = "http://schemas.microsoft.com/office/2016/09/relationships/commentsIds"
	extRel  = "http://schemas.microsoft.com/office/2018/08/relationships/commentsExtensible"
	exType  = "application/vnd.openxmlformats-officedocument.wordprocessingml.commentsExtended+xml"
	exPart  = "word/commentsExtended.xml"
	maxPara = 0x7FFFFFFF
)

// loadThreads reads the extended comment parts and links each comment to its thread.
func (d *docxDoc) loadThreads() {
	if d.cmtXML == nil {
		return
	}
	load := func(part string) *XMLDoc {
		if part == "" || !d.pkg.Has(part) {
			return nil
		}
		x, err := d.pkg.XML(part)
		if err != nil {
			return nil
		}
		return x
	}
	d.cmtExXML, d.cmtIDsXML, d.cmtExtXML = load(d.cmtExPart), load(d.cmtIDsPart), load(d.cmtExtPart)
	byPara := map[string]*docxComment{}
	for _, c := range d.comments {
		ps := c.node.KidsNamed("p")
		if len(ps) > 0 {
			c.lastPara = ps[len(ps)-1]
			c.paraID = c.lastPara.Attr("paraId")
		}
		if c.paraID != "" {
			byPara[strings.ToUpper(c.paraID)] = c
		}
	}
	if d.cmtExXML == nil {
		return
	}
	for _, ex := range d.cmtExXML.Root.KidsNamed("commentEx") {
		c := byPara[strings.ToUpper(ex.Attr("paraId"))]
		if c == nil {
			continue
		}
		c.ex = ex
		c.resolved = ex.Attr("done") == "1" || ex.Attr("done") == "true"
		if parent := byPara[strings.ToUpper(ex.Attr("paraIdParent"))]; parent != nil && parent != c {
			c.parent = parent
			parent.replies = append(parent.replies, c)
		}
	}
}

// label describes a comment for listings: its thread and state.
func (c *docxComment) label() string {
	var parts []string
	if c.parent != nil {
		parts = append(parts, "reply to comment:"+c.parent.id)
	}
	if c.resolved {
		parts = append(parts, "resolved")
	}
	if len(c.replies) > 0 {
		parts = append(parts, fmt.Sprintf("%d replies", len(c.replies)))
	}
	if !c.anchored && c.parent == nil {
		parts = append(parts, "no anchor")
	}
	s := fmt.Sprintf("comment:%s by %s", c.id, c.author)
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

func (e *docxEdit) commentAt(target string) (*docxComment, error) {
	m := commentTargetRe.FindStringSubmatch(target)
	if m == nil {
		return nil, refuse("target %q is not a comment address such as comment:3", target)
	}
	for _, c := range e.d.comments {
		if c.id == m[1] {
			return c, nil
		}
	}
	return nil, refuse("no comment %s; read range \"comments\" lists them", m[1])
}

// newHexID gives an 8-hex id below 0x80000000 that the document does not use yet, derived from seed so a batch gives
// the same ids every time.
func (e *docxEdit) newHexID(seed string) string {
	if e.usedIDs == nil {
		e.usedIDs = map[string]bool{}
		see := func(n *Node) bool {
			for _, a := range n.Attrs {
				if a.Name.Local == "paraId" || a.Name.Local == "textId" || a.Name.Local == "durableId" {
					e.usedIDs[strings.ToUpper(a.Value)] = true
				}
			}
			return true
		}
		for _, s := range e.d.stories {
			s.xml.Root.Walk(see)
		}
		for _, x := range []*XMLDoc{e.d.cmtXML, e.d.cmtExXML, e.d.cmtIDsXML, e.d.cmtExtXML} {
			if x != nil {
				x.Root.Walk(see)
			}
		}
	}
	for i := 0; ; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", seed, i)))
		v := binary.BigEndian.Uint32(h[:]) & maxPara
		id := fmt.Sprintf("%08X", v)
		if v != 0 && !e.usedIDs[id] {
			e.usedIDs[id] = true
			return id
		}
	}
}

// ensureW14 declares the w14 namespace on the comments part's root, once.
func (e *docxEdit) ensureW14() {
	if e.w14Done || e.d.cmtXML == nil {
		return
	}
	e.w14Done = true
	root := e.d.cmtXML.Root
	if root.resolve("w14") == w14NS {
		return
	}
	e.cmtSpans = append(e.cmtSpans, setAttrSpan(e.d.cmtXML.Data, root, "xmlns:w14", w14NS))
}

// paraIDOf gives a comment's paraId, adding one to its last paragraph when it has none.
func (e *docxEdit) paraIDOf(c *docxComment) (string, error) {
	if c.paraID != "" {
		return c.paraID, nil
	}
	if id, ok := e.newParaIDs[c]; ok {
		return id, nil
	}
	if c.lastPara == nil {
		return "", refuse("comment:%s has no paragraph to carry a thread id", c.id)
	}
	e.ensureW14()
	id := e.newHexID("para:" + c.id)
	e.cmtSpans = append(e.cmtSpans, setAttrSpan(e.d.cmtXML.Data, c.lastPara, "w14:paraId", id))
	e.newParaIDs[c] = id
	if e.d.cmtIDsXML != nil {
		e.idsAdd = append(e.idsAdd, fmt.Sprintf(`<w16cid:commentId xmlns:w16cid="%s" w16cid:paraId="%s" w16cid:durableId="%s"/>`, w16cid, id, e.newHexID("durable:"+c.id)))
	}
	return id, nil
}

// setDone sets or adds a comment's commentEx entry.
func (e *docxEdit) setDone(c *docxComment, done bool, parentPara string) error {
	val := map[bool]string{true: "1", false: "0"}[done]
	if c.ex != nil {
		if parentPara != "" && c.ex.Attr("paraIdParent") == "" {
			e.exSpans = append(e.exSpans, setAttrSpan(e.d.cmtExXML.Data, c.ex, c.ex.Tag("paraIdParent"), parentPara))
		}
		if c.ex.Attr("done") != val {
			e.exSpans = append(e.exSpans, setAttrSpan(e.d.cmtExXML.Data, c.ex, c.ex.Tag("done"), val))
		}
		return nil
	}
	pid, err := e.paraIDOf(c)
	if err != nil {
		return err
	}
	e.addEx(pid, parentPara, done)
	return nil
}

func (e *docxEdit) addEx(pid, parentPara string, done bool) {
	parent := ""
	if parentPara != "" {
		parent = fmt.Sprintf(` w15:paraIdParent="%s"`, parentPara)
	}
	e.exAdd = append(e.exAdd, fmt.Sprintf(`<w15:commentEx xmlns:w15="%s" w15:paraId="%s"%s w15:done="%s"/>`, w15NS, pid, parent, map[bool]string{true: "1", false: "0"}[done]))
}

// replyComment answers a comment. Word keeps threads one level deep, so a reply to a reply answers the thread's first
// comment. The reply gets its own range and reference next to its parent's, as Word writes them.
func (e *docxEdit) replyComment(ed Edit) error {
	c, err := e.commentAt(ed.Target)
	if err != nil {
		return err
	}
	if ed.Text == "" {
		return refuse("reply_comment needs text")
	}
	if e.deletedComments[c] {
		return refuse("comment:%s is being deleted in this batch", c.id)
	}
	root := c
	for root.parent != nil {
		root = root.parent
	}
	parentPara, err := e.paraIDOf(root)
	if err != nil {
		return err
	}
	if root.ex == nil && !e.exFor[root] {
		e.exFor[root] = true
		e.addEx(parentPara, "", root.resolved)
	}
	e.ensureW14()
	id := e.id()
	pid := e.newHexID("reply:" + id + ":" + ed.Text)
	n := root.node
	date := nowFunc().Format("2006-01-02T15:04:05Z")
	e.comment = append(e.comment, fmt.Sprintf(`<%s %s="%s" %s="%s" %s="%s" %s="%s"><%s w14:paraId="%s"><%s>%s</%s></%s></%s>`,
		n.Tag("comment"), n.Tag("id"), id, n.Tag("author"), escAttr(authorOf(ed)), n.Tag("date"), date, n.Tag("initials"), "AI",
		n.Tag("p"), pid, n.Tag("r"), runContent(n, ed.Text, "t"), n.Tag("r"), n.Tag("p"), n.Tag("comment")))
	e.addEx(pid, parentPara, root.resolved)
	if e.d.cmtIDsXML != nil {
		durable := e.newHexID("durable:" + pid)
		e.idsAdd = append(e.idsAdd, fmt.Sprintf(`<w16cid:commentId xmlns:w16cid="%s" w16cid:paraId="%s" w16cid:durableId="%s"/>`, w16cid, pid, durable))
		if e.d.cmtExtXML != nil {
			e.extAdd = append(e.extAdd, fmt.Sprintf(`<w16cex:commentExtensible xmlns:w16cex="%s" w16cex:durableId="%s" w16cex:dateUtc="%s"/>`, w16cex, durable, date))
		}
	}
	// The reply's markers go right after the parent's.
	for _, s := range e.d.stories {
		s.xml.Root.Walk(func(k *Node) bool {
			if !isW(k) || k.Attr("id") != root.id {
				return true
			}
			switch k.Local {
			case "commentRangeStart", "commentRangeEnd":
				e.spans[s] = append(e.spans[s], Span{k.End, k.End, fmt.Sprintf(`<%s %s="%s"/>`, k.Tag(k.Local), k.Tag("id"), id)})
			case "commentReference":
				at := k.End
				if r := k.Parent; r != nil && r.Local == "r" {
					at = r.End
				}
				e.spans[s] = append(e.spans[s], Span{at, at, fmt.Sprintf(`<%s><%s %s="%s"/></%s>`, k.Tag("r"), k.Tag("commentReference"), k.Tag("id"), id, k.Tag("r"))})
			}
			return true
		})
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Added comment:%s, a reply to comment:%s.", id, root.id))
	return nil
}

// resolveComment marks a thread resolved or open again: its first comment and every reply.
func (e *docxEdit) resolveComment(ed Edit) error {
	c, err := e.commentAt(ed.Target)
	if err != nil {
		return err
	}
	root := c
	for root.parent != nil {
		root = root.parent
	}
	done := ed.Op == "resolve_comment"
	if e.exFor[root] {
		return refuse("comment:%s already changes in this batch; send its state in a separate call", root.id)
	}
	e.exFor[root] = true
	if err := e.setDone(root, done, ""); err != nil {
		return err
	}
	rootPara, _ := e.paraIDOf(root)
	for _, r := range root.replies {
		if err := e.setDone(r, done, rootPara); err != nil {
			return err
		}
	}
	verb := map[bool]string{true: "Resolved", false: "Reopened"}[done]
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("%s the thread of comment:%s (%d replies).", verb, root.id, len(root.replies)))
	return nil
}

// dropThreadEntries removes a deleted comment's entries from the extended parts.
func (e *docxEdit) dropThreadEntries(c *docxComment) {
	if c.paraID == "" {
		return
	}
	drop := func(x *XMLDoc, local string, spans *[]Span) {
		if x == nil {
			return
		}
		for _, k := range x.Root.KidsNamed(local) {
			if strings.EqualFold(k.Attr("paraId"), c.paraID) {
				*spans = append(*spans, Span{k.Start, k.End, ""})
				if local == "commentId" && e.d.cmtExtXML != nil {
					for _, x2 := range e.d.cmtExtXML.Root.KidsNamed("commentExtensible") {
						if strings.EqualFold(x2.Attr("durableId"), k.Attr("durableId")) {
							e.extSpans = append(e.extSpans, Span{x2.Start, x2.End, ""})
						}
					}
				}
			}
		}
	}
	drop(e.d.cmtExXML, "commentEx", &e.exSpans)
	drop(e.d.cmtIDsXML, "commentId", &e.idsSpans)
}

// writeThreads writes the extended parts, creating commentsExtended when a thread needs it.
func (e *docxEdit) writeThreads(c *Changes) error {
	d := e.d
	put := func(x *XMLDoc, part string, spans []Span, add []string) {
		if x == nil || (len(spans) == 0 && len(add) == 0) {
			return
		}
		all := append([]Span(nil), spans...)
		if len(add) > 0 {
			all = append(all, insertKidSpan(x.Data, x.Root, strings.Join(add, "")))
		}
		c.Set[part] = Splice(x.Data, all)
	}
	put(d.cmtIDsXML, d.cmtIDsPart, e.idsSpans, e.idsAdd)
	put(d.cmtExtXML, d.cmtExtPart, e.extSpans, e.extAdd)
	if d.cmtExXML != nil {
		put(d.cmtExXML, d.cmtExPart, e.exSpans, e.exAdd)
		return nil
	}
	if len(e.exAdd) == 0 {
		return nil
	}
	part := exPart
	for i := 2; d.pkg.Has(part); i++ {
		part = fmt.Sprintf("word/commentsExtended%d.xml", i)
	}
	c.Set[part] = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<w15:commentsEx xmlns:w15="` + w15NS + `" xmlns:mc="` + mcNS + `" mc:Ignorable="w15">` + strings.Join(e.exAdd, "") + `</w15:commentsEx>`)
	if err := c.addContentType(d.pkg, part, exType); err != nil {
		return err
	}
	_, err := c.addRelationship(d.pkg, d.mainPart, exRel, relTargetFrom(d.mainPart, part), false)
	return err
}
