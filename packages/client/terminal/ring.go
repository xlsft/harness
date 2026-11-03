package terminal

import "bytes"

// ring keeps the last len(buf) bytes of a terminal's output, addressed by absolute offsets (bytes written since
// the start), so each viewer can read from where it left off and tell how much it missed. Not safe for
// concurrent use: the terminal's mutex guards it.
type ring struct {
	buf []byte
	end int64 // total bytes written
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size)} }

// start is the oldest offset still held.
func (r *ring) start() int64 { return max(0, r.end-int64(len(r.buf))) }

func (r *ring) write(p []byte) {
	n := len(r.buf)
	if len(p) >= n {
		r.end += int64(len(p) - n)
		p = p[len(p)-n:]
	}
	at := int(r.end % int64(n))
	c := copy(r.buf[at:], p)
	copy(r.buf, p[c:])
	r.end += int64(len(p))
}

// readFrom returns up to max bytes from off. When off is older than what the ring holds, reading resumes at a
// safe boundary after the oldest byte held and skipped says how many bytes were passed over.
func (r *ring) readFrom(off int64, max int) (data []byte, next, skipped int64) {
	if off > r.end {
		off = r.end
	}
	if s := r.start(); off < s {
		safe := r.safeStart(s)
		skipped, off = safe-off, safe
	}
	n := int(min(int64(max), r.end-off))
	if n <= 0 {
		return nil, off, skipped
	}
	data = make([]byte, n)
	size := int64(len(r.buf))
	at := int(off % size)
	c := copy(data, r.buf[at:])
	copy(data[c:], r.buf)
	return data, off + int64(n), skipped
}

// safeBoundaryWindow is how far past a cut the replay looks for a line break.
const safeBoundaryWindow = 4096

// safeStart picks where to resume after output was cut at s: just after the first line break within
// safeBoundaryWindow (escape sequences rarely span lines, so the terminal starts in a sane state), or else the
// first byte that does not continue a UTF-8 sequence.
func (r *ring) safeStart(s int64) int64 {
	n := int(min(int64(safeBoundaryWindow), r.end-s))
	if n <= 0 {
		return s
	}
	head := make([]byte, n)
	size := int64(len(r.buf))
	at := int(s % size)
	c := copy(head, r.buf[at:])
	copy(head[c:], r.buf)
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		return s + int64(i) + 1
	}
	i := 0
	for i < n && i < 4 && head[i]&0xC0 == 0x80 {
		i++
	}
	return s + int64(i)
}
