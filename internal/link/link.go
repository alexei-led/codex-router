package link

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/statedir"
)

// SocketName is the brain's link socket inside the home's state dir.
const SocketName = "brain.sock"

// pathFile records the link socket when it lives outside the home's state
// dir; the edge reads it through Path.
const pathFile = "brain.path"

// Path is the link socket of the brain whose home state dir is dir:
// SocketName in dir, or the path the brain recorded there because that one
// was too long for a unix socket. The edge reads it before each dial.
func Path(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, pathFile)); err == nil {
		if p := strings.TrimSpace(string(b)); filepath.IsAbs(p) {
			return p
		}
	}
	return filepath.Join(dir, SocketName)
}

// RecordPath writes pathFile when the link socket p is outside dir, and
// removes a stale one when it is not.
func RecordPath(dir, p string) error {
	rec := filepath.Join(dir, pathFile)
	if filepath.Dir(p) == dir {
		if err := os.Remove(rec); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := statedir.WriteAtomic(dir, pathFile, []byte(p+"\n")); err != nil {
		return fmt.Errorf("record link path: %w", err)
	}
	return nil
}

// SocketMode is the permission the socket is created with.
const SocketMode os.FileMode = 0o600

// Frame types. The edge sends hello, decide, passthrough and observe; the brain
// answers hello, rewrite or none per decide, and error before it closes a
// connection it refuses.
const (
	TypeHello       = "hello"
	TypeDecide      = "decide"
	TypeRewrite     = "rewrite"
	TypeNone        = "none"
	TypeObserve     = "observe"
	TypePassThrough = "passthrough"
	TypeError       = "error"
)

// MaxHeaderBytes caps a frame's JSON header.
const MaxHeaderBytes = 64 << 10

// MaxFrameBytes caps a frame after its length prefix: a header, its newline
// and a body as large as the proxy accepts.
const MaxFrameBytes = MaxHeaderBytes + 1 + proxy.MaxBodyBytes

// Header is a frame's JSON header. ID pairs a decide with its rewrite or
// none. Meta holds parsed turn metadata only, never raw request headers.
// DeadlineUnixMs is when the edge stops waiting for a decide's answer, in
// Unix milliseconds. It is absolute because edge and brain share the host's
// clock: the brain's deadline then covers the frame's transit and parse too,
// and its classifier call cannot outlive the edge's wait.
type Header struct {
	Type           string             `json:"type"`
	ID             uint64             `json:"id,omitempty"`
	HomeID         string             `json:"home_id,omitempty"`
	Meta           *responses.Meta    `json:"meta,omitempty"`
	DeadlineUnixMs int64              `json:"deadline_unix_ms,omitempty"`
	Rewrite        *responses.Rewrite `json:"rewrite,omitempty"`
	Served         string             `json:"served,omitempty"`
	Usage          *responses.Usage   `json:"usage,omitempty"`
	Status         int                `json:"status,omitempty"`
	Error          string             `json:"error,omitempty"`
}

var errFrame = errors.New("link: bad frame")

// WriteFrame writes one frame: a big-endian uint32 length, the header as one
// JSON line, then body. The body is written as is, without a copy.
func WriteFrame(w io.Writer, h Header, body []byte) error {
	hb, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("link: encode header: %w", err)
	}
	if len(hb) > MaxHeaderBytes {
		return fmt.Errorf("%w: header is %d bytes", errFrame, len(hb))
	}
	n := len(hb) + 1 + len(body)
	if n > MaxFrameBytes {
		return fmt.Errorf("%w: frame is %d bytes", errFrame, n)
	}
	head := make([]byte, 4, 4+len(hb)+1)
	binary.BigEndian.PutUint32(head, uint32(n))
	head = append(append(head, hb...), '\n')
	bufs := net.Buffers{head, body}
	_, err = bufs.WriteTo(w)
	return err
}

// ReadFrame reads one frame written by WriteFrame. An empty body is nil.
func ReadFrame(r io.Reader) (Header, []byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return Header{}, nil, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n > MaxFrameBytes {
		return Header{}, nil, fmt.Errorf("%w: frame is %d bytes", errFrame, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Header{}, nil, err
	}
	end := bytes.IndexByte(buf, '\n')
	if end < 0 || end > MaxHeaderBytes {
		return Header{}, nil, fmt.Errorf("%w: no header line", errFrame)
	}
	var h Header
	if err := json.Unmarshal(buf[:end], &h); err != nil || h.Type == "" {
		return Header{}, nil, fmt.Errorf("%w: header is not a typed JSON object", errFrame)
	}
	body := buf[end+1:]
	if len(body) == 0 {
		body = nil
	}
	return h, body, nil
}
