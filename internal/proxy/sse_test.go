package proxy

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestSSESplitter(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{
			name:   "two LF events in one chunk",
			chunks: []string{"data: a\n\ndata: b\n\n"},
			want:   []string{"data: a\n\n", "data: b\n\n"},
		},
		{
			name:   "CRLF events",
			chunks: []string{"data: a\r\n\r\ndata: b\r\n\r\n"},
			want:   []string{"data: a\r\n\r\n", "data: b\r\n\r\n"},
		},
		{
			name:   "separator split across chunks",
			chunks: []string{"data: a\n", "\nda", "ta: b\r\n\r", "\n"},
			want:   []string{"data: a\n\n", "data: b\r\n\r\n"},
		},
		{
			name:   "trailing partial event held back",
			chunks: []string{"data: a\n\ndata: par"},
			want:   []string{"data: a\n\n"},
		},
		{
			name:   "byte by byte",
			chunks: strings.Split("event: x\ndata: 1\n\n", ""),
			want:   []string{"event: x\ndata: 1\n\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			sp := &sseSplitter{onEvent: func(ev []byte) { got = append(got, string(ev)) }}
			for _, c := range tt.chunks {
				sp.write([]byte(c))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("events = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSSESplitterSkipsOversizedEvent(t *testing.T) {
	var got []string
	sp := &sseSplitter{onEvent: func(ev []byte) { got = append(got, string(ev)) }}
	huge := bytes.Repeat([]byte("x"), maxEventBytes)

	sp.write([]byte("data: "))
	sp.write(huge)
	sp.write([]byte("y\n"))
	sp.write([]byte("\ndata: next\n\n"))

	if !slices.Equal(got, []string{"data: next\n\n"}) {
		t.Errorf("events = %d, want only the event after the oversized one", len(got))
	}
	if cap(sp.buf) > 2*maxEventBytes+64 {
		t.Errorf("buffer cap %d grew past the bound", cap(sp.buf))
	}
}
