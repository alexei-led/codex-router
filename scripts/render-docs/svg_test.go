package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestParseANSI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want [][]run
	}{
		{"plain", "ab\ncd", [][]run{{{"ab", style{}}}, {{"cd", style{}}}}},
		{"256 foreground", "\x1b[38;5;196mx\x1b[0m", [][]run{{{"x", style{fg: "#ff0000"}}}}},
		{"truecolor background", "\x1b[48;2;1;2;3mx", [][]run{{{"x", style{bg: "#010203"}}}}},
		{"bold then reset", "\x1b[1ma\x1b[mb", [][]run{{{"a", style{bold: true}}, {"b", style{}}}}},
		{"dim and reverse", "\x1b[2;7mz", [][]run{{{"z", style{dim: true, reverse: true}}}}},
		{"bright 16 color", "\x1b[91mz", [][]run{{{"z", style{fg: "#f14c4c"}}}}},
		{"gray ramp", "\x1b[38;5;240mz", [][]run{{{"z", style{fg: "#585858"}}}}},
		{"other CSI dropped", "a\x1b[2Kb", [][]run{{{"ab", style{}}}}},
		{"truncated escape", "a\x1b[38;5", [][]run{{{"a", style{}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseANSI(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("lines = %v, want %v", got, tt.want)
			}
			for i := range got {
				if len(got[i]) != len(tt.want[i]) {
					t.Fatalf("line %d = %v, want %v", i, got[i], tt.want[i])
				}
				for j := range got[i] {
					if got[i][j] != tt.want[i][j] {
						t.Errorf("line %d run %d = %v, want %v", i, j, got[i][j], tt.want[i][j])
					}
				}
			}
		})
	}
}

func TestToSVGEscapesAndStyles(t *testing.T) {
	lines := parseANSI("\x1b[1;38;5;196m<a & \"b\">\x1b[0m ok\n\x1b[7;38;5;33m go \x1b[0m")
	svg := toSVG(lines, "T <1>", "D & more")

	if err := xml.Unmarshal([]byte(svg), new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("svg is not well-formed XML: %v\n%s", err, svg)
	}
	for _, want := range []string{
		"&lt;a &amp; &quot;b&quot;&gt;",
		`fill="#ff0000" font-weight="700"`,
		"<title id=\"title\">T &lt;1&gt;</title>",
		"D &amp; more",
		// Reverse video: the foreground color becomes the background rect.
		`fill="#0087ff"/>`,
		`fill="` + canvasBG + `" xml:space="preserve"> go </text>`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("svg lacks %q\n%s", want, svg)
		}
	}
	if strings.Contains(svg, "<a &") {
		t.Error("raw text reached the svg unescaped")
	}
}

func TestToSVGSizeFollowsGrid(t *testing.T) {
	svg := toSVG(parseANSI("abcd\nab"), "t", "d")
	if want := `viewBox="0 0 63 66"`; !strings.Contains(svg, want) {
		t.Errorf("svg lacks %s\n%s", want, svg)
	}
}

func TestSideBySide(t *testing.T) {
	left := [][]run{{{"ab", style{}}}, {{"c", style{}}}}
	right := [][]run{{{"1", style{}}}, {{"2", style{}}}, {{"3", style{}}}}
	got := sideBySide(left, right)
	want := []string{"   │ 1", "ab │ 2", "c  │ 3"}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i, l := range got {
		var b strings.Builder
		for _, r := range l {
			b.WriteString(r.text)
		}
		if b.String() != want[i] {
			t.Errorf("row %d = %q, want %q", i, b.String(), want[i])
		}
	}
}
