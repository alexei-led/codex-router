// Package hook implements `codex-router hook`, the optional Codex command hook
// (UserPromptSubmit, Stop) that prints a systemMessage when the route changed
// since the previous turn. It reads the control snapshot and never emits
// additionalContext.
package hook
