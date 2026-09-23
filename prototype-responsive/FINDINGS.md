# Responsive prototype findings

Question: which responsive layout makes named Worksheets, a contextual action bar, and multiline Insert Mode feel clearest at both ordinary and half-screen 4K terminal sizes?

## Variant verdict

- Preferred variant or combination:
- Header:
- Worksheet navigation:
- Preview and Insert Mode:
- Status and action bar:
- What to discard:

## Size checks

- 80×24: all three variants fit without vertical overflow. Split Workbench shows ten slots plus a compact preview in normal mode, then temporarily shows six slots to make room for the multiline editor.
- 120×36: Split Workbench uses the intended side-by-side grid and preview/editor; Ledger and Focus switch without losing selection or answers.
- 160×45 or actual half-screen size: Focus Canvas uses a centered, capped-width three-column layout instead of stretching text across the terminal.

## Editor checks

- Cursor visibility: the Bubbles virtual cursor is visible and blinks in the agent-controlled PTY. Production can promote this to the v2 real cursor where supported.
- Word wrapping: a long answer wrapped within both the 80-column and 120-column editor panels without altering the stored text.
- Newline keys: `Ctrl-J` inserted a hard newline. `Ctrl-Enter` still requires an enhanced-keyboard terminal and was not available in the test PTY.
- Save-and-stay versus save-and-advance: `Esc` saved the multiline value and kept Answer Slot 11 selected; the grid preview immediately reflected the hard newline.
- Navigation and deletion: `Ctrl-W` removed the preceding word. Structural insertion/deletion, renumbering, undo, `G`, and `gg` completed without a panic.

## Agent smoke-test corrections

- Corrected pane-width accounting after the first 120×36 pass exposed terminal wrapping caused by border and padding cells.
- Added a compact narrow-screen preview after the first 80×24 pass showed that ten grid rows plus a full preview could not fit.
- Accepted both a combined `gg` text event and two timed `g` events because PTYs and multiplexers may deliver rapid input in either form.

## Decision

- Keep:
- Change:
- Open question:
