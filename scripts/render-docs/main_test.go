package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const imagesDir = "../../docs/images"

func TestRenderMatchesTheCommittedImages(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no rsvg-convert: SVGs only
	out := t.TempDir()
	if err := render(out, filepath.Join(imagesDir, "src", "codex-footer.txt"), filepath.Join(imagesDir, "src", "codex-session.txt")); err != nil {
		t.Fatal(err)
	}

	got, _ := filepath.Glob(filepath.Join(out, "*"))
	want, _ := filepath.Glob(filepath.Join(imagesDir, "*.svg"))
	names := func(paths []string) []string {
		var n []string
		for _, p := range paths {
			n = append(n, filepath.Base(p))
		}
		return n
	}
	if !slices.Equal(names(got), names(want)) {
		t.Fatalf("rendered %q, committed %q", names(got), names(want))
	}
	for _, name := range names(want) {
		rendered, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join(imagesDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(rendered) != string(committed) {
			t.Errorf("docs/images/%s is stale: run go run ./scripts/render-docs", name)
		}
	}
}

func TestRenderFailsOnAMissingCapture(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := render(t.TempDir(), filepath.Join(t.TempDir(), "missing.txt"), filepath.Join(imagesDir, "src", "codex-session.txt")); err == nil {
		t.Error("render with a missing footer capture succeeded")
	}
}
