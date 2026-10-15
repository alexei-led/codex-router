package facts

import (
	"runtime"
	"strings"
	"testing"
)

func TestClipCutsBetweenMultiByteRunes(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{"two-byte runes", "αβγδεζηθικλμνξο", 11, "αβγ […] νξο"},
		{"four-byte runes", "😀😁😂🤣😃😄😅😆😉😊", 8, "😀😁 […] 😊"},
		{"mixed widths at both cuts", "a€😀bcdefgh😉€z", 11, "a€😀 […] 😉€z"},
		{"head only", "日本語テキスト", 3, "日本語"},
		{"invalid bytes become U+FFFD", "\xffabcdefghij\xfe", 9, "�a […] j�"},
		{"within limit is kept as is", "\xffабв", 4, "\xffабв"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clip(tt.text, tt.limit); got != tt.want {
				t.Errorf("clip(%q, %d) = %q, want %q", tt.text, tt.limit, got, tt.want)
			}
		})
	}
}

func runeClip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	marker := []rune(clipMarker)
	if limit <= len(marker) {
		return string(runes[:max(0, limit)])
	}
	room := limit - len(marker)
	head := (room + 1) / 2
	return string(runes[:head]) + clipMarker + string(runes[len(runes)-(room-head):])
}

func TestClipMatchesRuneClip(t *testing.T) {
	texts := []string{
		"", "a", "0123456789abcdefghij", "привет, как дела?", "😀a😁b😂c🤣d😃e😄",
		"\xe2\x82", "ab\xe2\x82\xacc\xe2\x80\x80\x80d", "\xf0\x9f\x98\xff\xc3", "x\xed\xa0\x80y€z",
	}
	for _, text := range texts {
		for limit := -1; limit <= len(text)+2; limit++ {
			if got, want := clip(text, limit), runeClip(text, limit); got != want {
				t.Errorf("clip(%q, %d) = %q, want %q", text, limit, got, want)
			}
		}
	}
}

func TestClipOfHugeTextAllocatesOnlyTheResult(t *testing.T) {
	text := strings.Repeat("я", 1<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := clip(text, 1200)
	runtime.ReadMemStats(&after)

	if n := len([]rune(got)); n != 1200 {
		t.Fatalf("clip kept %d runes, want 1200", n)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<10 {
		t.Errorf("clip allocated %d bytes for a %d-byte text, want under 64 KiB", alloc, len(text))
	}
}
