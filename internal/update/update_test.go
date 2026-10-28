package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testOS   = "darwin"
	testArch = "arm64"
)

func script(version string) string { return "#!/bin/sh\necho " + version + "\n" }

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type release struct {
	latest   string
	binary   string // body served for the asset
	checksum string // hash listed in checksums.txt; empty means sum(binary)
	noSums   bool
}

func (r release) server(t *testing.T) *httptest.Server {
	t.Helper()
	asset := AssetName(testOS, testArch)
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/releases/tag/"+r.latest, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/", func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/"+checksumsAsset) && !r.noSums:
			c := r.checksum
			if c == "" {
				c = sum(r.binary)
			}
			_, _ = w.Write([]byte("0000  other-asset\n" + c + "  " + asset + "\n"))
		case strings.HasSuffix(req.URL.Path, "/"+asset):
			_, _ = w.Write([]byte(r.binary))
		default:
			http.NotFound(w, req)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T, r release, mutate func(*Options)) (Options, *bytes.Buffer) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "codex-router")
	if err := os.WriteFile(target, []byte(script("v0.10.1")), 0o755); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	o := Options{
		Releases: r.server(t).URL + "/releases", Current: "v0.10.1", Target: target,
		GOOS: testOS, GOARCH: testArch, Out: out,
	}
	if mutate != nil {
		mutate(&o)
	}
	return o, out
}

func installed(t *testing.T, o Options) string {
	t.Helper()
	b, err := os.ReadFile(o.Target)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunInstalls(t *testing.T) {
	cases := []struct {
		name   string
		tag    string
		latest string
		served string
	}{
		{"latest", "", "v0.11.0", "v0.11.0"},
		{"pinned older tag", "v0.9.0", "v0.11.0", "v0.9.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, out := setup(t, release{latest: tc.latest, binary: script(tc.served)}, func(o *Options) { o.Tag = tc.tag })
			if err := Run(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			if got := installed(t, o); got != script(tc.served) {
				t.Errorf("target = %q, want the %s binary", got, tc.served)
			}
			if fi, _ := os.Stat(o.Target); fi.Mode().Perm() != 0o755 {
				t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
			}
			if !strings.Contains(out.String(), "v0.10.1 -> "+tc.served) {
				t.Errorf("output = %q", out.String())
			}
			left, _ := filepath.Glob(filepath.Join(filepath.Dir(o.Target), ".codex-router-update-*"))
			if len(left) != 0 {
				t.Errorf("staging files left behind: %v", left)
			}
		})
	}
}

func TestRunLeavesTargetAloneWhen(t *testing.T) {
	cases := []struct {
		name    string
		r       release
		mutate  func(*Options)
		wantErr string
	}{
		{"checksum differs", release{latest: "v0.11.0", binary: script("v0.11.0"), checksum: strings.Repeat("a", 64)}, nil, "checksum mismatch"},
		{"asset not in checksums", release{latest: "v0.11.0", binary: script("v0.11.0")}, func(o *Options) { o.GOARCH = "riscv64" }, "no entry"},
		{"checksums missing", release{latest: "v0.11.0", binary: script("v0.11.0"), noSums: true}, nil, "status 404"},
		{"binary reports another version", release{latest: "v0.11.0", binary: script("v0.12.0")}, nil, "reports"},
		{"binary does not run", release{latest: "v0.11.0", binary: "not an executable"}, nil, "run downloaded binary"},
		{"tag is malformed", release{latest: "v0.11.0", binary: script("v0.11.0")}, func(o *Options) { o.Tag = "../v1" }, "want vX.Y.Z"},
		{"latest is not a tag", release{latest: "nightly", binary: script("v0.11.0")}, nil, "not a release tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := setup(t, tc.r, tc.mutate)
			err := Run(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want mention of %q", err, tc.wantErr)
			}
			if got := installed(t, o); got != script("v0.10.1") {
				t.Errorf("target changed to %q", got)
			}
		})
	}
}

func TestRunSkipsWhenCurrent(t *testing.T) {
	o, out := setup(t, release{latest: "v0.10.1", binary: script("v0.99.0")}, nil)
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got := installed(t, o); got != script("v0.10.1") || !strings.Contains(out.String(), "already at v0.10.1") {
		t.Errorf("target = %q, output = %q; want no download", got, out.String())
	}
}

func TestRunCheckOnlyReports(t *testing.T) {
	o, out := setup(t, release{latest: "v0.11.0", binary: script("v0.11.0")}, func(o *Options) { o.Check = true })
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got := installed(t, o); got != script("v0.10.1") || !strings.Contains(out.String(), "release v0.11.0") {
		t.Errorf("target = %q, output = %q", got, out.String())
	}
}

func TestChecksumFor(t *testing.T) {
	sums := "ABC123  codex-router-linux-amd64\ndef456 *codex-router-darwin-arm64\n\nbad line\n"
	cases := []struct{ asset, want string }{
		{"codex-router-linux-amd64", "abc123"},
		{"codex-router-darwin-arm64", "def456"},
		{"codex-router-linux-arm64", ""},
	}
	for _, tc := range cases {
		got, err := checksumFor(sums, tc.asset)
		if (err != nil) != (tc.want == "") || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.asset, got, err, tc.want)
		}
	}
}
