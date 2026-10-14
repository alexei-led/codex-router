// Package setup routes one Codex home through a stage 2 edge and brain
// (codex-router setup), and undoes it. It edits the home's config.toml in
// marked blocks (plan M10) with the router provider and its token header
// (M9), stores the provider port and token in the home's state dir, and
// installs that home's service units through internal/service.
package setup
