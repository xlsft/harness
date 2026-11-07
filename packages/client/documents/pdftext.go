package documents

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Text extraction from a page's content stream (ISO 32000 sections 9.4 and 9.10): the text-showing operators, decoded
// through each font's ToUnicode map when it has one and as Latin-1 when not. Good enough to search, to measure pages
// and to diff. It does not lay text out; a line break is any move to a new line.

type pdfFont struct {
	twoByte bool
	cmap    map[uint32]string
}

func (f *pdfFont) decode(s []byte, undecodable *bool) string {
	if f == nil {
		return latin1(s)
	}
	var b strings.Builder
	if f.twoByte {
		for i := 0; i+1 < len(s); i += 2 {
			code := uint32(s[i])<<8 | uint32(s[i+1])
			if t, ok := f.cmap[code]; ok {
				b.WriteString(t)
			} else {
				*undecodable = true
			}
		}
		return b.String()
	}
	for _, c := range s {
		if t, ok := f.cmap[uint32(c)]; ok {
			b.WriteString(t)
		} else {
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

func latin1(s []byte) string {
	r := make([]rune, len(s))
	for i, c := range s {
		r[i] = rune(c)
	}
	return string(r)
}

type pdfTok struct {
	kind  byte // 'o' operator, 's' string, 'n' number, 'a' start of array, 'z' end of array, 'p' other
	text  string
	bytes []byte
	num   float64
}

// pdfLexer tokenizes just enough of a content stream: dictionaries and inline images are skipped.
type pdfLexer struct {
	d  []byte
	at int
}

func isPDFSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isPDFDelim(c byte) bool {
	return strings.IndexByte("()<>[]{}/%", c) >= 0
}

func (l *pdfLexer) next() (pdfTok, bool) {
	for l.at < len(l.d) {
		c := l.d[l.at]
		switch {
		case isPDFSpace(c):
			l.at++
		case c == '%':
			for l.at < len(l.d) && l.d[l.at] != '\n' && l.d[l.at] != '\r' {
				l.at++
			}
		case c == '(':
			return pdfTok{kind: 's', bytes: l.literal()}, true
		case c == '<':
			if l.at+1 < len(l.d) && l.d[l.at+1] == '<' {
				l.at += 2
				return pdfTok{kind: 'p'}, true
			}
			return pdfTok{kind: 's', bytes: l.hex()}, true
		case c == '>':
			l.at++
			if l.at < len(l.d) && l.d[l.at] == '>' {
				l.at++
			}
			return pdfTok{kind: 'p'}, true
		case c == '[':
			l.at++
			return pdfTok{kind: 'a'}, true
		case c == ']':
			l.at++
			return pdfTok{kind: 'z'}, true
		case c == '/':
			start := l.at
			l.at++
			for l.at < len(l.d) && !isPDFSpace(l.d[l.at]) && !isPDFDelim(l.d[l.at]) {
				l.at++
			}
			return pdfTok{kind: 'p', text: string(l.d[start:l.at])}, true
		case c == '{' || c == '}' || c == ')':
			l.at++
		default:
			start := l.at
			for l.at < len(l.d) && !isPDFSpace(l.d[l.at]) && !isPDFDelim(l.d[l.at]) {
				l.at++
			}
			word := string(l.d[start:l.at])
			if f, err := strconv.ParseFloat(word, 64); err == nil {
				return pdfTok{kind: 'n', num: f, text: word}, true
			}
			if word == "BI" { // an inline image: skip to its end
				if end := bytes.Index(l.d[l.at:], []byte("EI")); end >= 0 {
					l.at += end + 2
				} else {
					l.at = len(l.d)
				}
				return pdfTok{kind: 'o', text: "BI"}, true
			}
			return pdfTok{kind: 'o', text: word}, true
		}
	}
	return pdfTok{}, false
}

func (l *pdfLexer) literal() []byte {
	l.at++ // (
	var out []byte
	depth := 1
	for l.at < len(l.d) {
		c := l.d[l.at]
		l.at++
		switch c {
		case '\\':
			if l.at >= len(l.d) {
				return out
			}
			e := l.d[l.at]
			l.at++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\n':
			case '\r':
				if l.at < len(l.d) && l.d[l.at] == '\n' {
					l.at++
				}
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for i := 0; i < 2 && l.at < len(l.d) && l.d[l.at] >= '0' && l.d[l.at] <= '7'; i++ {
						v = v*8 + int(l.d[l.at]-'0')
						l.at++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, e)
				}
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return out
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func (l *pdfLexer) hex() []byte {
	l.at++ // <
	var out []byte
	hi, have := byte(0), false
	for l.at < len(l.d) {
		c := l.d[l.at]
		l.at++
		if c == '>' {
			break
		}
		v, ok := hexVal(c)
		if !ok {
			continue
		}
		if have {
			out = append(out, hi<<4|v)
			have = false
		} else {
			hi, have = v, true
		}
	}
	if have {
		out = append(out, hi<<4)
	}
	return out
}

// extractText runs the page's operators. It returns the text and the images counted, adding inline images to images.
func extractText(content []byte, fonts map[string]*pdfFont, images int, undecodable *bool) (string, int) {
	l := &pdfLexer{d: content}
	var b strings.Builder
	var font *pdfFont
	var operands []pdfTok
	inArray := false
	var arr []pdfTok
	writeStr := func(s []byte) { b.WriteString(font.decode(s, undecodable)) }
	newline := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
	}
	for {
		t, ok := l.next()
		if !ok {
			break
		}
		switch t.kind {
		case 'a':
			inArray, arr = true, nil
			continue
		case 'z':
			inArray = false
			operands = append(operands, pdfTok{kind: 'a'})
			continue
		case 'o':
		default:
			if inArray {
				arr = append(arr, t)
			} else {
				operands = append(operands, t)
			}
			continue
		}
		switch t.text {
		case "Tf":
			for i := len(operands) - 1; i >= 0; i-- {
				if operands[i].kind == 'p' && strings.HasPrefix(operands[i].text, "/") {
					font = fonts[operands[i].text[1:]]
					break
				}
			}
		case "Tj":
			if n := len(operands); n > 0 && operands[n-1].kind == 's' {
				writeStr(operands[n-1].bytes)
			}
		case "'", "\"":
			newline()
			if n := len(operands); n > 0 && operands[n-1].kind == 's' {
				writeStr(operands[n-1].bytes)
			}
		case "TJ":
			for _, x := range arr {
				if x.kind == 's' {
					writeStr(x.bytes)
				} else if x.kind == 'n' && x.num < -200 {
					// A large negative kern is a gap: most producers write it for a space.
					if !strings.HasSuffix(b.String(), " ") {
						b.WriteByte(' ')
					}
				}
			}
			arr = nil
		case "Td", "TD", "T*", "ET":
			newline()
		case "Tm":
			newline()
		case "BI":
			images++
		}
		operands = operands[:0]
	}
	return strings.TrimSpace(b.String()), images
}

// parseToUnicode reads a ToUnicode CMap's bfchar and bfrange sections.
func parseToUnicode(data []byte) map[uint32]string {
	out := map[uint32]string{}
	l := &pdfLexer{d: data}
	code := func(b []byte) uint32 {
		var v uint32
		for _, c := range b {
			v = v<<8 | uint32(c)
		}
		return v
	}
	var toks []pdfTok
	mode := ""
	for {
		t, ok := l.next()
		if !ok {
			break
		}
		if t.kind == 'o' {
			switch t.text {
			case "beginbfchar":
				mode, toks = "char", nil
			case "beginbfrange":
				mode, toks = "range", nil
			case "endbfchar":
				for i := 0; i+1 < len(toks); i += 2 {
					if toks[i].kind == 's' && toks[i+1].kind == 's' {
						out[code(toks[i].bytes)] = utf16be(toks[i+1].bytes)
					}
				}
				mode = ""
			case "endbfrange":
				for i := 0; i+2 < len(toks); {
					if toks[i].kind != 's' || toks[i+1].kind != 's' {
						i++
						continue
					}
					lo, hi := code(toks[i].bytes), code(toks[i+1].bytes)
					if toks[i+2].kind == 's' {
						base := []rune(utf16be(toks[i+2].bytes))
						if len(base) > 0 && hi-lo < 65536 {
							for c := lo; c <= hi; c++ {
								r := append([]rune(nil), base...)
								r[len(r)-1] += rune(c - lo)
								out[c] = string(r)
							}
						}
						i += 3
						continue
					}
					// An array of destinations.
					j := i + 2
					if toks[j].kind == 'a' {
						j++
					}
					c := lo
					for j < len(toks) && toks[j].kind == 's' && c <= hi {
						out[c] = utf16be(toks[j].bytes)
						c++
						j++
					}
					if j < len(toks) && toks[j].kind == 'z' {
						j++
					}
					i = j
				}
				mode = ""
			}
			continue
		}
		if mode != "" {
			toks = append(toks, t)
		}
	}
	return out
}

func utf16be(b []byte) string {
	if len(b)%2 != 0 {
		return latin1(b)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	return string(utf16.Decode(u))
}
