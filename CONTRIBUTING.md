# Contributing

Use Go 1.27.3 or newer. Keep changes small. For bug reports, include the router version, Codex version, OS, classifier and steps to reproduce. Remove credentials and private text from logs before sharing them.

## Build and check

```sh
make build
make tools
make fmt-check vet lint test vuln
```

`make build` writes `.bin/codex-router`. `make test` runs the race detector and writes coverage to `coverage.out`. Tool versions are pinned in the Makefile. CI runs tests on macOS and Linux.

Use a focused `go test ./internal/<package>` while changing code. Add a behavior test for a bug before fixing it. Do not use real credentials in fixtures.

## Documentation and images

Keep user tasks in the [user guide](docs/user-guide.md), fields in [configuration](docs/configuration.md), and internals in [architecture](docs/architecture.md). Put measurements and their limits in [evaluation](docs/evaluation.md).

Regenerate terminal illustrations after changing the panel:

```sh
go run ./scripts/render-docs
```

The generator uses the real panel renderer with sample data. Install `rsvg-convert` to regenerate the PNG files as well as SVG. The images carry a sample-data label. Open them before committing. `go test ./scripts/render-docs` checks the SVG files against the generator.

## Live checks

`scripts/live/stage1.sh` and `scripts/live/stage2.sh` are maintainer harnesses, not installation scripts. They need agterm, Codex and separate test homes under `~/.cache/codex-router-live`. Read their headers before running them. Some commands reset test config files and start or stop user services. Never point them at a normal Codex home.

`scripts/spikes/` holds the probes those harnesses and archived reports use. Historical runs live in [the archive](docs/archive/README.md). They are evidence for the versions tested, not a current compatibility guarantee.

## Release

1. Confirm the next version does not already exist. Update `plugins/codex-router/.codex-plugin/plugin.json` to match it without the leading `v`.
2. Run the checks above and `make release-check VERSION=vX.Y.Z`. Review the diff and write user-facing notes.
3. Commit the release changes. Create an annotated tag with the reviewed notes, then push the main commit and tag.
4. The tag workflow validates, cross-builds and publishes the GitHub release. Verify the workflow, four binaries, checksums and version strings.

The annotated tag is the release notes source. Do not move a published tag or publish around the workflow. See [release automation](.github/workflows/release.yml).

### Homebrew release updates

The release workflow dispatches `update-formula` to `alexei-led/homebrew-tap`
after the GitHub release and its assets are published. The dispatch omits a
version so the tap resolves the latest stable release, including when an older
release job is retried. The tap downloads all four binaries, computes their SHA256 checksums, and commits the formula update.
The initial `Formula/codex-router.rb` must be merged into the tap before this
job runs. A successful dispatch only confirms acceptance; check the tap's
**Auto-update Formula** workflow separately.

Configure the `TAP_GITHUB_TOKEN` Actions secret in this repository. Use a
fine-grained personal access token owned by an account with write access to
`alexei-led/homebrew-tap`, restricted to that repository with **Contents: Read
and write**. The default `GITHUB_TOKEN` cannot dispatch to another repository.
No additional Codex plugin hook trust is needed for Homebrew installation.

You can reuse an existing tap token if it has that scope, is unexpired, and its
use by this repository is acceptable. GitHub cannot reveal an existing secret's
value: retrieve it from your password manager and add the same value through
**Settings → Secrets and variables → Actions → New repository secret**, named
`TAP_GITHUB_TOKEN`, or use the hidden CLI prompt:

```sh
gh secret set TAP_GITHUB_TOKEN --repo alexei-led/codex-router
```

Do not put the token in a command argument, file in the repository, or log.
If the value is no longer available, create a new tap-scoped token rather than
rotating a shared token and breaking other projects. Repository secrets are
independent copies; future rotation must update every repository using it.

To retry a failed tap update after fixing the cause, rerun only the failed
`update-homebrew` job. Do not rerun the already-successful release publisher.
The tap also supports **Actions → Auto-update Formula → Run workflow**, with
formula `codex-router` and the published version without its `v` prefix.
