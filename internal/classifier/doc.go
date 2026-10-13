// Package classifier asks an external model which tier and activity a prompt
// needs. It holds the shared contract (criteria, instructions, advice shape),
// one adapter per wire protocol (system-one, ollama), and a client with a
// total deadline, two attempts, and a breaker. Input carries only the prompt
// and clipped dialogue: Codex headers cannot reach an adapter by construction.
// Ported from claude-model-router lib/classifier-*.mjs.
package classifier
