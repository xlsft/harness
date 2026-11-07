package documents

import (
	"strings"
	"testing"
)

func TestDOCXCommentThreads(t *testing.T) {
	data := docxFixture(t, false, "")
	res := apply(t, "a.docx", data, Edit{Op: "reply_comment", Target: "comment:5", Text: "Done, see p2", Author: "Bob"})
	noErrors(t, "a.docx", res.Data)
	if !hasPart(t, res.Data, exPart) || !strings.Contains(partOf(t, res.Data, "word/_rels/document.xml.rels"), exRel) || !strings.Contains(partOf(t, res.Data, contentTypesPart), exType) {
		t.Fatal("commentsExtended was not created and related")
	}
	cx := partOf(t, res.Data, "word/comments.xml")
	if !strings.Contains(cx, `xmlns:w14="`+w14NS+`"`) || strings.Count(cx, "w14:paraId=") != 2 {
		t.Fatalf("paragraph ids:\n%s", cx)
	}
	doc := partOf(t, res.Data, "word/document.xml")
	if !strings.Contains(doc, `<w:commentRangeStart w:id="5"/><w:commentRangeStart w:id="13"/>`) || !strings.Contains(doc, `<w:commentReference w:id="5"/></w:r><w:r><w:commentReference w:id="13"/></w:r>`) {
		t.Fatalf("reply markers:\n%s", doc)
	}
	d := mustOpen(t, "a.docx", res.Data)
	s := d.Structure()
	if !strings.Contains(s, "comment:5 by Ann (1 replies): Check this") || !strings.Contains(s, "comment:13 by Bob (reply to comment:5): Done, see p2") {
		t.Fatalf("structure:\n%s", s)
	}
	// A reply to the reply answers the thread.
	again := apply(t, "a.docx", res.Data, Edit{Op: "reply_comment", Target: "comment:13", Text: "Thanks"})
	if s := readAll(t, mustOpen(t, "a.docx", again.Data), "comments"); !strings.Contains(s, "(reply to comment:5): Thanks") {
		t.Errorf("nested reply:\n%s", s)
	}
	noErrors(t, "a.docx", again.Data)

	// Resolve and reopen the thread: both entries change, nothing else in the comments.
	resolved := apply(t, "a.docx", res.Data, Edit{Op: "resolve_comment", Target: "comment:13"})
	onlyChanged(t, res.Data, resolved.Data, exPart)
	if n := strings.Count(partOf(t, resolved.Data, exPart), `w15:done="1"`); n != 2 {
		t.Errorf("%d entries done", n)
	}
	if s := readAll(t, mustOpen(t, "a.docx", resolved.Data), "comments"); !strings.Contains(s, "comment:5 by Ann (resolved, 1 replies)") {
		t.Errorf("resolved:\n%s", s)
	}
	open := apply(t, "a.docx", resolved.Data, Edit{Op: "reopen_comment", Target: "comment:5"})
	if strings.Contains(partOf(t, open.Data, exPart), `w15:done="1"`) {
		t.Error("not reopened")
	}

	// Resolving a comment that never had a thread creates its entry.
	alone := apply(t, "a.docx", data, Edit{Op: "resolve_comment", Target: "comment:5"})
	noErrors(t, "a.docx", alone.Data)
	if !strings.Contains(partOf(t, alone.Data, exPart), `w15:done="1"`) {
		t.Error("no entry for a resolved comment")
	}

	// Deleting the thread's first comment deletes its replies, their markers and entries.
	del := apply(t, "a.docx", res.Data, Edit{Op: "delete_comment", Target: "comment:5"})
	noErrors(t, "a.docx", del.Data)
	if strings.Contains(partOf(t, del.Data, "word/comments.xml"), "w:comment ") || strings.Contains(partOf(t, del.Data, exPart), "commentEx ") || strings.Contains(partOf(t, del.Data, "word/document.xml"), "commentRangeStart") {
		t.Error("the thread was not deleted whole")
	}
	refused(t, "a.docx", data, "no comment", Edit{Op: "reply_comment", Target: "comment:99", Text: "x"})
	refused(t, "a.docx", data, "needs text", Edit{Op: "reply_comment", Target: "comment:5"})
}

// A file from a newer Word has durable ids: new comments get them too.
func TestDOCXCommentThreadsWithDurableIDs(t *testing.T) {
	base := docxFixture(t, false, "")
	pkg, err := OpenPackage(base)
	if err != nil {
		t.Fatal(err)
	}
	c := NewChanges()
	c.Set["word/commentsIds.xml"] = []byte(xmlHeader + `<w16cid:commentsIds xmlns:w16cid="` + w16cid + `"></w16cid:commentsIds>`)
	c.Set["word/commentsExtensible.xml"] = []byte(xmlHeader + `<w16cex:commentsExtensible xmlns:w16cex="` + w16cex + `"></w16cex:commentsExtensible>`)
	if err := c.addContentType(pkg, "word/commentsIds.xml", wordMain+"commentsIds+xml"); err != nil {
		t.Fatal(err)
	}
	if err := c.addContentType(pkg, "word/commentsExtensible.xml", wordMain+"commentsExtensible+xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.addRelationship(pkg, "word/document.xml", idsRel, "commentsIds.xml", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.addRelationship(pkg, "word/document.xml", extRel, "commentsExtensible.xml", false); err != nil {
		t.Fatal(err)
	}
	data, err := pkg.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	res := apply(t, "a.docx", data, Edit{Op: "reply_comment", Target: "comment:5", Text: "ok"})
	noErrors(t, "a.docx", res.Data)
	if n := strings.Count(partOf(t, res.Data, "word/commentsIds.xml"), "w16cid:durableId="); n != 2 {
		t.Errorf("%d durable ids", n)
	}
	if !strings.Contains(partOf(t, res.Data, "word/commentsExtensible.xml"), "w16cex:dateUtc=") {
		t.Error("no extensible entry")
	}
	del := apply(t, "a.docx", res.Data, Edit{Op: "delete_comment", Target: "comment:5"})
	if strings.Contains(partOf(t, del.Data, "word/commentsIds.xml"), "durableId") || strings.Contains(partOf(t, del.Data, "word/commentsExtensible.xml"), "durableId") {
		t.Error("durable ids of deleted comments stayed")
	}
}
