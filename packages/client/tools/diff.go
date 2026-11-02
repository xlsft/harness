package tools

import (
	"fmt"
	"strings"
)

// Hunk is one hunk of a line diff in Claude Code's structuredPatch shape.
type Hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// maxDiffCells bounds the LCS table; larger changed regions are reported
// as a single replacement.
const maxDiffCells = 4_000_000

func diffSplit(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
}

// lineOps computes an edit script between a and b.
func lineOps(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var ops []diffOp
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{' ', l})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma)*len(mb) > maxDiffCells {
		for _, l := range ma {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, diffOp{'+', l})
		}
	} else {
		ops = append(ops, lcsOps(ma, mb)...)
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	w := m + 1
	t := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i*w+j] = t[(i+1)*w+j+1] + 1
			} else if t[(i+1)*w+j] >= t[i*w+j+1] {
				t[i*w+j] = t[(i+1)*w+j]
			} else {
				t[i*w+j] = t[i*w+j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case t[(i+1)*w+j] >= t[i*w+j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// DiffLines returns hunks turning oldText into newText with ctx lines of
// context around each change.
func DiffLines(oldText, newText string, ctx int) []Hunk {
	ops := lineOps(diffSplit(oldText), diffSplit(newText))
	var hunks []Hunk
	oldLn, newLn := make([]int, len(ops)), make([]int, len(ops))
	o, n := 1, 1
	for i, op := range ops {
		oldLn[i], newLn[i] = o, n
		if op.kind != '+' {
			o++
		}
		if op.kind != '-' {
			n++
		}
	}
	var changes []int
	for i, op := range ops {
		if op.kind != ' ' {
			changes = append(changes, i)
		}
	}
	for k := 0; k < len(changes); {
		first, last := changes[k], changes[k]
		k++
		for k < len(changes) && changes[k]-last <= 2*ctx+1 {
			last = changes[k]
			k++
		}
		start, stop := max(0, first-ctx), min(len(ops), last+ctx+1)
		h := Hunk{OldStart: oldLn[start], NewStart: newLn[start]}
		for _, op := range ops[start:stop] {
			h.Lines = append(h.Lines, string(op.kind)+op.text)
			if op.kind != '+' {
				h.OldLines++
			}
			if op.kind != '-' {
				h.NewLines++
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
}

// UnifiedDiff renders hunks as unified diff text, keeping at most maxLines
// lines (0 = unlimited).
func UnifiedDiff(hunks []Hunk, maxLines int) string {
	var b strings.Builder
	n := 0
	for _, h := range hunks {
		lines := append([]string{fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)}, h.Lines...)
		for _, l := range lines {
			if maxLines > 0 && n >= maxLines {
				b.WriteString("… (diff truncated)\n")
				return b.String()
			}
			b.WriteString(l)
			b.WriteByte('\n')
			n++
		}
	}
	return b.String()
}

// DiffStats counts added and removed lines.
func DiffStats(hunks []Hunk) (added, removed int) {
	for _, h := range hunks {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	}
	return
}
