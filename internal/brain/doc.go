// Package brain is the stage 2 router of one Codex home (plan decision M6).
// It runs the route engine for the home's edge over the link socket, serves
// control protocol v2 as a kind=brain endpoint in the registry, and follows
// the main threads of the home's shared Codex daemon: it resumes each one to
// get its notifications and cwd, mirrors routes into the Codex footers, and
// unsubscribes from threads whose TUI is gone so the daemon can unload them.
// A lost daemon connection is redialed, and every thread is resubscribed and
// its footer resynced.
//
// A restarted brain starts with no learned state (plan M11). The user's
// intent (pauses, manual holds, pending pins) is kept in intent.json and
// reloaded; an unreadable file pauses the home and flags it as state lost.
// Each thread's mode override is kept in the mode store. Each resumed thread is seeded with the route Codex reports and held
// there until a reply measures its cache; so is a thread whose request,
// already deep in a conversation, arrives before its resume.
package brain
