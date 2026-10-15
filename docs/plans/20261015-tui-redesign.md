# Panel redesign

## Goal

One question drives the panel: **which model is each Codex session using right now, why, and should I step in?** Everything else (policy, classifier, usage) is secondary and one keystroke away.

Inputs: UX review of v0.4 (classifier off guesses, static hints, tabs that split what users compare), the claude-model-router pane (decision and evidence on one line, savings against a named baseline, honest qualifiers, tier strip), and TUI practice (lazygit/k9s/Textual footers, btop accelerators, NN/g on tabs and toasts).

## Principles

1. Decision first, evidence on the same line: tier, activity → model · effort, reason, support against its bar.
2. Act on the selection. Arrow keys or a click select a session; verbs act on it. No tabbing through buttons.
3. One definition per action: a `key.Binding` drives Update, the footer, the help overlay, button accelerators and clicks.
4. Shape and color together. Every state has a glyph (`● ‖ ◐ ↑ ? ! ○ ✕`); it reads under `NO_COLOR`.
5. Whitespace and dim section headers, no nested boxes.
6. Errors stay inline with one next step. Toasts only confirm an action, for 2.5 s.

## Screens

Three views: **Sessions** (default), **Policy**, **Usage**, plus a `?` help overlay. View names are clickable and cycle with `tab`/`shift+tab`. No numbers on labels.

### Sessions (stacked, under 120 columns)

The list on top, the selected session's detail below, on one screen.

```
codex-router  3 sessions · 2 homes · Jev ready                  Sessions  Policy  Usage   ? help

  SESSION                       STATE      MODEL                 LAST TURNS     SWITCH
● default
▸ codex-router · main           ↑ high     gpt-6.1-sol · high    ▁▃▃▅▇▇▅▃▅▇     18:42 ↑
  codex-router · fix/hook       ● on       gpt-6-luna · medium   ▃▃▁▁▃▃▃▁▁▃     18:29 ↓
  notes                         ‖ paused   gpt-6-luna · medium   ▃▃▃
● work · brain up 2h
  reflex · main                 ○ waiting for the first prompt

codex-router · main                                                       default · 3fa9c0d1
 HIGH  debug → gpt-6.1-sol · high                       ↑ same tool failure twice, edit between
       Jev high 99% ≥ 75% · switch ≈ 38K uncached tokens
 Pin   micro   low   medium  ▶high                                        next turn only
 Session  144 replies · 19 switches · saved ≈ $1.18 (22%) vs gpt-6-sol · medium
          ████████████▓▓▓▓▓░░░░███  4 replies per cell
 Context  ███░░░░░░░ 10%  91K / 828K      Cache  97% last reply · 64% session
 Look     cache reuse 64% after 19 switches · see Policy

 ↑↓ select · p pin · space pause · P pause home · tab views · ? help · q quit
```

- Header: totals and the focused home's classifier state; `✕ Jev unreachable · resumes in 42s` replaces `Jev ready` when it fails.
- STATE column: `● on`, `↑ high` (escalation), `‖ paused`, `◐ manual`, `? unknown`, `! state lost`, `✕ disconnected`, `○ waiting`. The tier sparkline `▁▃▅▇` = micro/low/medium/high reads without color.
- A router whose Codex has not sent a prompt yet shows as `○ waiting for the first prompt` under its home, not `no sessions`.
- Detail block: the band line (tier chip, activity → route, reason), the evidence line, the Pin row, session summary and strip, context and cache, one worth-a-look line. Classifier off: the chip reads `PINS ONLY` and the evidence line gives the exact reason.
- Pin row: buttons, the running tier marked `▶`, the pinned one reversed. `p` opens a popover `1 micro  2 low  3 medium  4 high  0 none`; a click on a button pins directly.

### Sessions (side by side, 120 columns and wider)

The list on the left (about 45%), the detail on the right. `tab` moves focus between them when the list is focused; the focused pane has an accent gutter `▎`.

### Policy

```
 Preset    thrifty  ▶balanced  quality        for every session of home default
 Classifier  ● Jev · jev-1.13.0 · api.typesafe.ai · 388 ms median · 0 failures
 Routes    micro  gpt-6-luna · low      low   gpt-6-luna · medium
           medium gpt-6-sol · medium    high  gpt-6.1-sol · high       group of gpt-6-sol
           debug at medium → gpt-6-sol · high
 Switching up after 2 votes, bar 0.75 · down after 2 votes, bar 0.90 · min dwell 8 requests
```

`s` opens the preset popover (`1 thrifty  2 balanced  3 quality`). Classifier off: `○ off · <exact reason> · requests keep Codex's model; only pins change it`.

### Usage

Savings as one sentence against the baseline, then a small ledger, then counts by tier and activity with block bars. Missing prices: `savings need "prices" in the config`.

### Help overlay

`?` draws a layer (Lip Gloss compositor) over the panel with every binding, grouped: Navigate, Session, Home, Views. `esc` or `?` closes it.

## Hints and keys

- Footer: one line, context-sensitive, built from the bindings that apply now; key bold accent, text muted, ends with `? help`.
- Buttons show an accelerator only where it is a single-key action (`space`, `P`); tier and preset choices use the popover's number keys, so no letter clashes (micro/medium).
- `↑↓`/`j k` select, `enter` opens the detail (narrow) or moves focus to it (wide), `esc` back, `tab`/`shift+tab` views, `[`/`]` previous/next session, `q` quit.
- Mouse: click selects, a second click on the selected row opens it; buttons and view names act on click; wheel scrolls the list. `--no-mouse` stays.

## Look

- Tokens, each a light/dark pair chosen on `tea.BackgroundColorMsg` (dark until it arrives): fg, muted, accent, surface (selection), ok, warn, err, info; tier colors micro mint, low blue, medium amber, high rose.
- Selection: `▸` gutter plus reverse on the selected row. Hover: none (cell motion only).
- Toast: bottom right, 2.5 s, only for confirmations (`pinned high for the next turn`).

## Out of scope

Editing routes or policy thresholds from the panel (the config file stays the source); a command palette; hover effects.
