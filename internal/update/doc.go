// Package update replaces the running codex-router binary with a release
// build from GitHub: it resolves the tag, downloads the asset for this
// platform, checks it against the release's checksums.txt, runs it once to
// confirm its version, and swaps it in with an atomic rename.
package update
