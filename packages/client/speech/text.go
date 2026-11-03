// Package speech runs dictation (speech to text) and "Read aloud" (text to speech) for the client: local engines
// on this machine (whisper.cpp, NVIDIA Parakeet through sherpa-onnx, macOS say, Piper), their model downloads,
// audio conversion, and the client of the server's harness.speech.v1.SpeechService for the global providers.
package speech

import (
	"regexp"
	"strings"
)

var (
	reFence       = regexp.MustCompile("(?ms)^[ \t]*(```|~~~)[^\n]*\n.*?(^[ \t]*(```|~~~)[ \t]*$|\\z)")
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reImage       = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink        = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	reRefLink     = regexp.MustCompile(`\[([^\]]+)\]\[[^\]]*\]`)
	reRefDef      = regexp.MustCompile(`(?m)^[ \t]*\[[^\]]+\]:\s+\S+.*$`)
	reAutoLink    = regexp.MustCompile(`<(https?://[^>]+)>`)
	reBareURL     = regexp.MustCompile(`https?://\S+`)
	reInlineCode  = regexp.MustCompile("`+([^`\n]+?)`+")
	reHeading     = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	reSetextRule  = regexp.MustCompile(`(?m)^[ \t]*(=+|-+)[ \t]*$`)
	reRule        = regexp.MustCompile(`(?m)^[ \t]*([-*_][ \t]*){3,}$`)
	reQuote       = regexp.MustCompile(`(?m)^[ \t]*>+[ \t]?`)
	reBullet      = regexp.MustCompile(`(?m)^[ \t]*[-*+][ \t]+(\[[ xX]\][ \t]+)?`)
	reOrdered     = regexp.MustCompile(`(?m)^[ \t]*(\d+)[.)][ \t]+`)
	reTableSep    = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t]*:?-{2,}:?[ \t]*(\|[ \t]*:?-{2,}:?[ \t]*)*\|?[ \t]*$`)
	reTableRow    = regexp.MustCompile(`(?m)^[ \t]*\|(.*)\|[ \t]*$`)
	reBold        = regexp.MustCompile(`(\*\*|__)(\S(?:.*?\S)?)(\*\*|__)`)
	reItalic      = regexp.MustCompile(`(^|[\s(])([*_])(\S(?:[^*_\n]*?\S)?)([*_])([\s).,;:!?]|$)`)
	reStrike      = regexp.MustCompile(`~~(.+?)~~`)
	reHTMLTag     = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	reSpaces      = regexp.MustCompile(`[ \t]+`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
)

// SpeakableText turns Markdown (a message as shown) into text worth reading aloud: code blocks are replaced by a
// short mention, links keep their text, URLs, images and HTML are dropped, emphasis markers, headings, list
// markers and table pipes are removed. Lines that end without punctuation get a full stop so engines pause.
func SpeakableText(md string) string {
	s := strings.ReplaceAll(md, "\r\n", "\n")
	s = reFence.ReplaceAllStringFunc(s, func(block string) string {
		lang := strings.TrimSpace(strings.TrimLeft(strings.SplitN(strings.TrimSpace(block), "\n", 2)[0], "`~"))
		if f := strings.Fields(lang); len(f) > 0 && len(f[0]) <= 20 {
			return "\n(" + f[0] + " code block omitted.)\n"
		}
		return "\n(Code block omitted.)\n"
	})
	s = reHTMLComment.ReplaceAllString(s, "")
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reRefLink.ReplaceAllString(s, "$1")
	s = reRefDef.ReplaceAllString(s, "")
	s = reAutoLink.ReplaceAllString(s, "")
	s = reBareURL.ReplaceAllString(s, "")
	s = reInlineCode.ReplaceAllString(s, "$1")
	s = reHeading.ReplaceAllString(s, "$1.")
	s = reTableSep.ReplaceAllString(s, "")
	s = reTableRow.ReplaceAllStringFunc(s, func(row string) string {
		cells := strings.Split(strings.Trim(strings.TrimSpace(row), "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		return strings.Join(cells, ", ") + "."
	})
	s = reRule.ReplaceAllString(s, "")
	s = reSetextRule.ReplaceAllString(s, "")
	s = reQuote.ReplaceAllString(s, "")
	s = reBullet.ReplaceAllString(s, "")
	s = reOrdered.ReplaceAllString(s, "$1. ")
	s = reBold.ReplaceAllString(s, "$2")
	s = reStrike.ReplaceAllString(s, "$1")
	// Twice: a match takes the space after it, which the next emphasis may need as its leading space.
	s = reItalic.ReplaceAllString(reItalic.ReplaceAllString(s, "$1$3$5"), "$1$3$5")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
		if l != "" && !strings.ContainsAny(l[len(l)-1:], ".!?:;,…)") && !strings.HasSuffix(l, "。") {
			l += "."
		}
		lines[i] = l
	}
	s = strings.Join(lines, "\n")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
