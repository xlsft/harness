package plugins

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HostMarker is where the frontend puts the theme and the bridge of the message API: a comment right after the CSP, the
// only comment the document keeps.
const HostMarker = "harness-host"

// Limits of a pane's document: its page, and all it inlines.
const (
	maxPageBytes  = 1 << 20
	maxPaneBytes  = 4 << 20
	maxAssetBytes = 2 << 20
)

// PaneCSP is the policy of a pane's document. Its own scripts and styles are inlined, so nothing is loaded from anywhere;
// the network is the origins given, which are the declared ones this device approved.
func PaneCSP(network []string) string {
	connect := "'none'"
	img := "data: blob:"
	if len(network) > 0 {
		connect = strings.Join(network, " ")
		img += " " + connect
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'unsafe-inline'",
		"style-src 'unsafe-inline'",
		"img-src " + img,
		"font-src data:",
		"media-src data: blob:",
		"connect-src " + connect,
		"object-src 'none'",
		"frame-src 'none'",
		"worker-src 'none'",
		"base-uri 'none'",
		"form-action 'none'",
	}, "; ")
}

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".svg": "image/svg+xml", ".webp": "image/webp"}

// PaneDocument builds the document of a pane: the page with the plugin's own scripts, stylesheets and images it names by a
// relative path inlined, comments, <base> and <meta http-equiv> (a refresh would navigate) removed, and the CSP as the first
// element of its head followed by the host marker. Files must stay inside the plugin's folder.
func PaneDocument(p *Plugin, pane UIPane, network []string) (doc, csp string, err error) {
	data, err := os.ReadFile(pane.Path)
	if err != nil {
		return "", "", err
	}
	if len(data) > maxPageBytes {
		return "", "", fmt.Errorf("the page of pane %s is larger than %d bytes", pane.ID, maxPageBytes)
	}
	root, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("pane %s: %w", pane.ID, err)
	}
	b := &inliner{plugin: p, dir: filepath.Dir(pane.Path), total: len(data)}
	b.walk(root)
	if b.err != nil {
		return "", "", fmt.Errorf("pane %s: %w", pane.ID, b.err)
	}
	head := find(root, atom.Head)
	if head == nil {
		return "", "", fmt.Errorf("pane %s: the page has no head", pane.ID)
	}
	csp = PaneCSP(network)
	first := head.FirstChild
	for _, n := range []*html.Node{
		{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{{Key: "charset", Val: "utf-8"}}},
		{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{{Key: "http-equiv", Val: "Content-Security-Policy"}, {Key: "content", Val: csp}}},
		{Type: html.CommentNode, Data: HostMarker},
	} {
		head.InsertBefore(n, first)
	}
	var out bytes.Buffer
	if err := html.Render(&out, root); err != nil {
		return "", "", err
	}
	return out.String(), csp, nil
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, a); f != nil {
			return f
		}
	}
	return nil
}

type inliner struct {
	plugin *Plugin
	dir    string
	total  int
	err    error
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

func dropAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, key) {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func (b *inliner) walk(n *html.Node) {
	for c := n.FirstChild; c != nil && b.err == nil; {
		next := c.NextSibling
		if b.remove(c) {
			n.RemoveChild(c)
		} else {
			b.inline(c)
			b.walk(c)
		}
		c = next
	}
}

// remove reports a node the document doesn't keep.
func (b *inliner) remove(n *html.Node) bool {
	switch {
	case n.Type == html.CommentNode:
		return true
	case n.Type != html.ElementNode:
		return false
	case n.DataAtom == atom.Base:
		return true
	case n.DataAtom == atom.Meta:
		_, equiv := attr(n, "http-equiv")
		return equiv
	case n.DataAtom == atom.Link:
		rel, _ := attr(n, "rel")
		return !strings.EqualFold(strings.TrimSpace(rel), "stylesheet")
	}
	return false
}

func (b *inliner) inline(n *html.Node) {
	if n.Type != html.ElementNode {
		return
	}
	switch n.DataAtom {
	case atom.Script:
		if src, ok := attr(n, "src"); ok {
			if data, ok := b.local(src); ok {
				dropAttr(n, "src")
				for c := n.FirstChild; c != nil; c = n.FirstChild {
					n.RemoveChild(c)
				}
				// Raw text ends at the first "</script": in JavaScript "<\/" means the same.
				n.AppendChild(&html.Node{Type: html.TextNode, Data: escapeEnd(string(data), "</script")})
			}
		}
	case atom.Link:
		if href, ok := attr(n, "href"); ok {
			if data, ok := b.local(href); ok {
				n.Type, n.Data, n.DataAtom = html.ElementNode, "style", atom.Style
				n.Attr = nil
				n.AppendChild(&html.Node{Type: html.TextNode, Data: escapeEnd(string(data), "</style")})
			}
		}
	case atom.Img:
		if src, ok := attr(n, "src"); ok {
			mime := imageTypes[strings.ToLower(filepath.Ext(strings.SplitN(src, "?", 2)[0]))]
			if mime == "" {
				return
			}
			if data, ok := b.local(src); ok {
				for i := range n.Attr {
					if strings.EqualFold(n.Attr[i].Key, "src") {
						n.Attr[i].Val = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
					}
				}
			}
		}
	}
}

// local reads a file the page names by a relative path; ok is false for anything else, which the CSP then refuses.
func (b *inliner) local(ref string) ([]byte, bool) {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" || strings.HasPrefix(u.Path, "/") {
		return nil, false
	}
	abs := filepath.Join(b.dir, filepath.FromSlash(u.Path))
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		b.err = fmt.Errorf("%s is not a file of the plugin", ref)
		return nil, false
	}
	root, err := filepath.EvalSymlinks(b.plugin.Root)
	if err != nil || !inside(real, root) || !isFile(real) {
		b.err = fmt.Errorf("%s is not a file of the plugin", ref)
		return nil, false
	}
	st, err := os.Stat(real)
	if err != nil || st.Size() > maxAssetBytes || b.total+int(st.Size()) > maxPaneBytes {
		b.err = fmt.Errorf("%s makes the pane larger than allowed", ref)
		return nil, false
	}
	data, err := os.ReadFile(real)
	if err != nil {
		b.err = err
		return nil, false
	}
	b.total += len(data)
	return data, true
}

// escapeEnd keeps inlined text from ending its element early, whatever the case of the closing tag.
func escapeEnd(s, end string) string {
	lower := strings.ToLower(s)
	if !strings.Contains(lower, end) {
		return s
	}
	var out strings.Builder
	for {
		i := strings.Index(lower, end)
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i+1])
		out.WriteString(`\`)
		s, lower = s[i+1:], lower[i+1:]
	}
}
