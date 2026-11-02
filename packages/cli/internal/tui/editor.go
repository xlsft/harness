package tui

import (
	"strings"
	"unicode"
)

// editor is the multiline prompt buffer: runes plus a cursor offset.
type editor struct {
	text   []rune
	cursor int
}

func (e *editor) String() string { return string(e.text) }
func (e *editor) Empty() bool    { return len(e.text) == 0 }

func (e *editor) Set(s string) {
	e.text = []rune(s)
	e.cursor = len(e.text)
}

func (e *editor) Reset() { e.text, e.cursor = nil, 0 }

func (e *editor) Insert(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	r := []rune(s)
	e.text = append(e.text[:e.cursor], append(r, e.text[e.cursor:]...)...)
	e.cursor += len(r)
}

func (e *editor) Backspace() {
	if e.cursor == 0 {
		return
	}
	e.text = append(e.text[:e.cursor-1], e.text[e.cursor:]...)
	e.cursor--
}

func (e *editor) Delete() {
	if e.cursor >= len(e.text) {
		return
	}
	e.text = append(e.text[:e.cursor], e.text[e.cursor+1:]...)
}

func (e *editor) Left() {
	if e.cursor > 0 {
		e.cursor--
	}
}

func (e *editor) Right() {
	if e.cursor < len(e.text) {
		e.cursor++
	}
}

// lineBounds returns the start and end offsets of the logical line containing the cursor.
func (e *editor) lineBounds() (int, int) {
	start := e.cursor
	for start > 0 && e.text[start-1] != '\n' {
		start--
	}
	end := e.cursor
	for end < len(e.text) && e.text[end] != '\n' {
		end++
	}
	return start, end
}

func (e *editor) Home() { e.cursor, _ = e.lineBounds() }
func (e *editor) End()  { _, e.cursor = e.lineBounds() }

// OnFirstLine / OnLastLine report whether the cursor is on the first / last logical line.
func (e *editor) OnFirstLine() bool {
	for i := 0; i < e.cursor; i++ {
		if e.text[i] == '\n' {
			return false
		}
	}
	return true
}

func (e *editor) OnLastLine() bool {
	for i := e.cursor; i < len(e.text); i++ {
		if e.text[i] == '\n' {
			return false
		}
	}
	return true
}

// Up / Down move the cursor to the same column of the previous / next logical line.
func (e *editor) Up() {
	start, _ := e.lineBounds()
	if start == 0 {
		return
	}
	col := e.cursor - start
	prevEnd := start - 1
	prevStart := prevEnd
	for prevStart > 0 && e.text[prevStart-1] != '\n' {
		prevStart--
	}
	e.cursor = min(prevStart+col, prevEnd)
}

func (e *editor) Down() {
	start, end := e.lineBounds()
	if end >= len(e.text) {
		return
	}
	col := e.cursor - start
	nextStart := end + 1
	nextEnd := nextStart
	for nextEnd < len(e.text) && e.text[nextEnd] != '\n' {
		nextEnd++
	}
	e.cursor = min(nextStart+col, nextEnd)
}

// DeleteWord deletes the word before the cursor (ctrl+w, alt+backspace).
func (e *editor) DeleteWord() {
	i := e.cursor
	for i > 0 && unicode.IsSpace(e.text[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.text[i-1]) {
		i--
	}
	e.text = append(e.text[:i], e.text[e.cursor:]...)
	e.cursor = i
}

// KillLine deletes from the start of the line to the cursor (ctrl+u) and returns the text.
func (e *editor) KillLine() string {
	start, _ := e.lineBounds()
	if start == e.cursor && start > 0 {
		start-- // join with the previous line
	}
	killed := string(e.text[start:e.cursor])
	e.text = append(e.text[:start], e.text[e.cursor:]...)
	e.cursor = start
	return killed
}

// WordBefore returns the token ending at the cursor and its start offset.
func (e *editor) WordBefore() (string, int) {
	i := e.cursor
	for i > 0 && !unicode.IsSpace(e.text[i-1]) {
		i--
	}
	return string(e.text[i:e.cursor]), i
}

// ReplaceRange replaces text[from:cursor] with s.
func (e *editor) ReplaceRange(from int, s string) {
	r := []rune(s)
	e.text = append(e.text[:from], append(r, e.text[e.cursor:]...)...)
	e.cursor = from + len(r)
}
