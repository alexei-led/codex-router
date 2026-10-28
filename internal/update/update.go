package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultReleases is the GitHub releases page of the project. Tests point
// Options.Releases at a fake.
const DefaultReleases = "https://github.com/alexei-led/codex-router/releases"

const (
	checksumsAsset   = "checksums.txt"
	maxChecksumBytes = 1 << 20
	maxBinaryBytes   = 100 << 20
	verifyTimeout    = 10 * time.Second
	requestTimeout   = 2 * time.Minute
)

var tagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Options configure Run.
type Options struct {
	// Releases is the releases page URL; empty means DefaultReleases.
	Releases string
	// Current is the running version, as `codex-router version` prints it.
	Current string
	// Target is the executable to replace.
	Target string
	// Tag is the release to install, vX.Y.Z; empty means the latest.
	Tag string
	// Check only reports the release to install.
	Check bool
	// GOOS and GOARCH choose the asset; empty means the build's values.
	GOOS, GOARCH string
	// Client is the HTTP client; nil means one with a request timeout.
	Client *http.Client
	Out    io.Writer
}

// AssetName is the release asset for an OS and architecture.
func AssetName(goos, goarch string) string {
	return "codex-router-" + goos + "-" + goarch
}

// Run installs the chosen release over o.Target. It prints what it did to
// o.Out and changes nothing unless the new binary passes every check.
func Run(ctx context.Context, o Options) error {
	if o.Releases == "" {
		o.Releases = DefaultReleases
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: requestTimeout}
	}
	if o.Tag != "" && !tagPattern.MatchString(o.Tag) {
		return fmt.Errorf("version %q: want vX.Y.Z", o.Tag)
	}
	tag := o.Tag
	if tag == "" {
		var err error
		if tag, err = latestTag(ctx, o); err != nil {
			return err
		}
	}
	switch {
	case o.Check:
		_, _ = fmt.Fprintf(o.Out, "installed %s, release %s\n", o.Current, tag)
		return nil
	case tag == o.Current:
		_, _ = fmt.Fprintf(o.Out, "already at %s\n", tag)
		return nil
	}
	if err := install(ctx, o, tag); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Out, "%s -> %s (%s)\n", o.Current, tag, o.Target)
	_, _ = fmt.Fprintln(o.Out, "Close running routed sessions and restart a set-up home's services to use it.")
	return nil
}

// latestTag reads the tag the latest-release redirect points at, without
// following it.
func latestTag(ctx context.Context, o Options) (string, error) {
	c := *o.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimSuffix(o.Releases, "/")+"/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	_ = resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("resolve latest release: status %d without a redirect", resp.StatusCode)
	}
	tag := path.Base(loc.Path)
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("resolve latest release: %q is not a release tag", tag)
	}
	return tag, nil
}

func install(ctx context.Context, o Options, tag string) error {
	asset := AssetName(orDefault(o.GOOS, goos()), orDefault(o.GOARCH, goarch()))
	base := strings.TrimSuffix(o.Releases, "/") + "/download/" + url.PathEscape(tag) + "/"

	sums, err := fetch(ctx, o.Client, base+checksumsAsset, maxChecksumBytes)
	if err != nil {
		return err
	}
	want, err := checksumFor(string(sums), asset)
	if err != nil {
		return fmt.Errorf("%s %s: %w", tag, checksumsAsset, err)
	}

	// Same directory as the target: rename is atomic only within a filesystem.
	dir := filepath.Dir(o.Target)
	tmp, err := os.CreateTemp(dir, ".codex-router-update-*")
	if err != nil {
		return fmt.Errorf("stage update in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	got, err := download(ctx, o.Client, base+asset, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s %s: checksum mismatch, not installed", tag, asset)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if err := confirmVersion(ctx, tmp.Name(), tag); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), o.Target)
}

// confirmVersion runs the staged binary: a wrong-platform or truncated file
// fails here, before it replaces a working one.
func confirmVersion(ctx context.Context, bin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Errorf("run downloaded binary: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("downloaded binary reports %q, want %q, not installed", got, tag)
	}
	return nil
}

func get(ctx context.Context, c *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", path.Base(u), err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %s: status %d", path.Base(u), resp.StatusCode)
	}
	return resp, nil
}

func fetch(ctx context.Context, c *http.Client, u string, limit int64) ([]byte, error) {
	resp, err := get(ctx, c, u)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", path.Base(u), err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download %s: over %d bytes", path.Base(u), limit)
	}
	return b, nil
}

// download copies the body to w and returns its SHA-256 in hex.
func download(ctx context.Context, c *http.Client, u string, w io.Writer) (string, error) {
	resp, err := get(ctx, c, u)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxBinaryBytes+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", path.Base(u), err)
	}
	if n > maxBinaryBytes {
		return "", fmt.Errorf("download %s: over %d bytes", path.Base(u), int64(maxBinaryBytes))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checksumFor finds asset's hash in sha256sum output: "<hex>  <name>".
func checksumFor(sums, asset string) (string, error) {
	for line := range strings.SplitSeq(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", errors.New("no entry for " + asset)
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
