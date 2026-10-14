package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Cell grid of the picture. Text is placed per run at an exact column, so
// font width drift cannot shift later runs on a line.
const (
	cellW    = 7.8
	cellH    = 19.0
	padX     = 16.0
	padY     = 14.0
	fontSize = 13

	canvasBG  = "#14171f"
	defaultFG = "#d0d0d0"
	frameLine = "#2e3340"
)

type style struct {
	fg, bg  string // "" means the default color
	bold    bool
	dim     bool
	reverse bool
}

type run struct {
	text string
	st   style
}

// parseANSI splits s into lines of style runs. It understands SGR (CSI ... m)
// only: reset, bold, dim, reverse, 16/256/truecolor foreground and
// background. Other escape sequences are dropped.
func parseANSI(s string) [][]run {
	var lines [][]run
	var cur []run
	var st style
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			cur = append(cur, run{text.String(), st})
			text.Reset()
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\x1b' && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && (rs[j] < '@' || rs[j] > '~') {
				j++
			}
			if j >= len(rs) {
				i = len(rs)
				break
			}
			if rs[j] == 'm' {
				flush()
				st = applySGR(st, string(rs[i+2:j]))
			}
			i = j
		case r == '\n':
			flush()
			lines = append(lines, cur)
			cur = nil
		case r == '\r':
		default:
			text.WriteRune(r)
		}
	}
	flush()
	if len(cur) > 0 {
		lines = append(lines, cur)
	}
	return lines
}

func applySGR(st style, params string) style {
	if params == "" {
		return style{}
	}
	p := strings.Split(params, ";")
	for k := 0; k < len(p); k++ {
		n, _ := strconv.Atoi(p[k]) // an empty parameter means 0
		switch {
		case n == 0:
			st = style{}
		case n == 1:
			st.bold = true
		case n == 2:
			st.dim = true
		case n == 22:
			st.bold, st.dim = false, false
		case n == 7:
			st.reverse = true
		case n == 27:
			st.reverse = false
		case n == 39:
			st.fg = ""
		case n == 49:
			st.bg = ""
		case n >= 30 && n <= 37:
			st.fg = xterm(n - 30)
		case n >= 90 && n <= 97:
			st.fg = xterm(n - 90 + 8)
		case n >= 40 && n <= 47:
			st.bg = xterm(n - 40)
		case n >= 100 && n <= 107:
			st.bg = xterm(n - 100 + 8)
		case n == 38 || n == 48:
			c, used := extendedColor(p[k+1:])
			k += used
			if n == 38 {
				st.fg = c
			} else {
				st.bg = c
			}
		}
	}
	return st
}

// extendedColor reads the arguments after 38 or 48: "5;n" or "2;r;g;b". It
// returns the color and how many arguments it used.
func extendedColor(a []string) (string, int) {
	at := func(i int) int {
		n, _ := strconv.Atoi(a[i])
		return min(max(n, 0), 255)
	}
	switch {
	case len(a) >= 2 && a[0] == "5":
		return xterm(at(1)), 2
	case len(a) >= 4 && a[0] == "2":
		return fmt.Sprintf("#%02x%02x%02x", at(1), at(2), at(3)), 4
	}
	return "", len(a)
}

var ansi16 = [16]string{
	"#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
	"#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
}

// xterm maps a 256-color index to a hex color.
func xterm(n int) string {
	switch {
	case n < 0 || n > 255:
		return ""
	case n < 16:
		return ansi16[n]
	case n < 232:
		n -= 16
		level := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + 40*v
		}
		return fmt.Sprintf("#%02x%02x%02x", level(n/36), level(n/6%6), level(n%6))
	}
	g := 8 + 10*(n-232)
	return fmt.Sprintf("#%02x%02x%02x", g, g, g)
}

func escapeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func runWidth(l []run) int {
	n := 0
	for _, r := range l {
		n += len([]rune(r.text))
	}
	return n
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// toSVG draws lines on a dark terminal-like canvas. Reverse video swaps the
// run's colors; dim lowers its opacity.
func toSVG(lines [][]run, title, desc string) string {
	cols := 0
	for _, l := range lines {
		cols = max(cols, runWidth(l))
	}
	w := float64(cols)*cellW + 2*padX
	h := float64(len(lines))*cellH + 2*padY
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role="img" aria-labelledby="title desc">`+"\n", w, h, w, h)
	fmt.Fprintf(&b, "  <title id=\"title\">%s</title>\n  <desc id=\"desc\">%s</desc>\n", escapeXML(title), escapeXML(desc))
	fmt.Fprintf(&b, "  <style>text { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'DejaVu Sans Mono', monospace; font-size: %dpx; }</style>\n", fontSize)
	fmt.Fprintf(&b, `  <rect width="%.0f" height="%.0f" rx="8" fill="%s"/>`+"\n", w, h, canvasBG)
	fmt.Fprintf(&b, `  <rect x="0.5" y="0.5" width="%.0f" height="%.0f" rx="8" fill="none" stroke="%s"/>`+"\n", w-1, h-1, frameLine)
	for row, l := range lines {
		col := 0
		base := padY + float64(row)*cellH + 14
		for _, r := range l {
			n := len([]rune(r.text))
			x := padX + float64(col)*cellW
			col += n
			fg, bg := r.st.fg, r.st.bg
			if r.st.reverse {
				fg, bg = orDefault(bg, canvasBG), orDefault(fg, defaultFG)
			}
			if bg != "" {
				fmt.Fprintf(&b, `  <rect x="%.1f" y="%.1f" width="%.1f" height="%.0f" fill="%s"/>`+"\n", x, base-14, float64(n)*cellW, cellH, bg)
			}
			if strings.TrimSpace(r.text) == "" {
				continue
			}
			fmt.Fprintf(&b, `  <text x="%.1f" y="%.1f" fill="%s"`, x, base, orDefault(fg, defaultFG))
			if r.st.bold {
				b.WriteString(` font-weight="700"`)
			}
			if r.st.dim {
				b.WriteString(` opacity="0.6"`)
			}
			fmt.Fprintf(&b, ` xml:space="preserve">%s</text>`+"\n", escapeXML(r.text))
		}
	}
	b.WriteString("</svg>\n")
	return b.String()
}
