// Package catalog reads the Codex model catalog (GET {base_url}/models, or
// $CODEX_HOME/models_cache.json read-only) and answers two questions: which
// models share a compatibility group, and how large a model's usable context
// window is. Field names follow models_cache.json as fetched by codex 0.162.1.
package catalog
