package proxy

import "bytes"

// maxEventBytes caps one buffered SSE event; a larger event (a huge
// response.completed) is skipped so a stream cannot grow memory unbounded.
const maxEventBytes = 8 << 20

var (
	sepLF   = []byte("\n\n")
	sepCRLF = []byte("\n\r\n")
)

// sseSplitter cuts a byte stream into SSE events (blank-line separated, LF
// or CRLF) and hands each complete event to onEvent.
type sseSplitter struct {
	onEvent  func([]byte)
	buf      []byte
	scanned  int
	skipping bool
}

func (sp *sseSplitter) write(p []byte) {
	sp.buf = append(sp.buf, p...)
	for {
		end := eventEnd(sp.buf, sp.scanned)
		if end < 0 {
			break
		}
		if !sp.skipping {
			sp.onEvent(sp.buf[:end])
		}
		sp.skipping = false
		sp.buf = append(sp.buf[:0], sp.buf[end:]...)
		sp.scanned = 0
	}
	if len(sp.buf) > maxEventBytes {
		// Keep the last two bytes: they may start a separator.
		sp.buf = append(sp.buf[:0], sp.buf[len(sp.buf)-2:]...)
		sp.skipping = true
	}
	// A separator is at most 3 bytes, so it can start 2 bytes before the end.
	sp.scanned = max(len(sp.buf)-2, 0)
}

// eventEnd returns the index just past the first event separator at or
// after from, or -1.
func eventEnd(b []byte, from int) int {
	rest := b[from:]
	lf, crlf := bytes.Index(rest, sepLF), bytes.Index(rest, sepCRLF)
	switch {
	case lf >= 0 && (crlf < 0 || lf < crlf):
		return from + lf + len(sepLF)
	case crlf >= 0:
		return from + crlf + len(sepCRLF)
	}
	return -1
}
