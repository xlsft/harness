package terminal

import (
	"strings"
	"testing"
)

func TestRingKeepsTheTail(t *testing.T) {
	r := newRing(8)
	r.write([]byte("abc"))
	if d, next, skipped := r.readFrom(0, 100); string(d) != "abc" || next != 3 || skipped != 0 {
		t.Fatalf("got %q %d %d", d, next, skipped)
	}
	r.write([]byte("defghij")) // 10 bytes written, "cdefghij" held
	if r.start() != 2 {
		t.Fatalf("start = %d", r.start())
	}
	if d, next, _ := r.readFrom(3, 100); string(d) != "defghij" || next != 10 {
		t.Fatalf("got %q %d", d, next)
	}
	if d, next, _ := r.readFrom(3, 2); string(d) != "de" || next != 5 {
		t.Fatalf("max: got %q %d", d, next)
	}
	// A write larger than the ring keeps its last bytes.
	r.write([]byte("0123456789XY"))
	if d, _, _ := r.readFrom(r.start(), 100); string(d) != "456789XY" {
		t.Fatalf("got %q", d)
	}
	if d, next, _ := r.readFrom(r.end+5, 100); d != nil || next != r.end {
		t.Fatalf("past the end: %q %d", d, next)
	}
}

func TestRingResumesAtASafeBoundary(t *testing.T) {
	r := newRing(16)
	r.write([]byte("\x1b[31mlong red line\nok\n")) // 22 bytes: the escape sequence and part of the line are cut
	d, next, skipped := r.readFrom(0, 100)
	if string(d) != "ok\n" || next != 22 || skipped != 19 {
		t.Fatalf("got %q next=%d skipped=%d", d, next, skipped)
	}

	// No line break: skip UTF-8 continuation bytes only.
	r = newRing(7)
	r.write([]byte("ab" + strings.Repeat("ж", 4))) // 10 bytes, 7 held: cut inside the first "ж"
	d, _, skipped = r.readFrom(0, 100)
	if string(d) != "жжж" || skipped != 4 {
		t.Fatalf("got %q skipped=%d", d, skipped)
	}
}
