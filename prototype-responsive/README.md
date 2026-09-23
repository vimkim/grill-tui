# Responsive Grill TUI prototype

> THROWAWAY PROTOTYPE — this is design evidence, not production code.

Question: **which responsive layout makes named Worksheets, a contextual action bar, and multiline Insert Mode feel clearest at both ordinary and half-screen 4K terminal sizes?**

Three terminal-native variants share one in-memory interaction model. Switch with `[` and `]`, or launch a reproducible variant with `--variant split`, `--variant ledger`, or `--variant focus`.

## Run

```sh
just prototype-responsive --name abc 11
```

The prototype intentionally has no persistence and does not touch the clipboard, SQLite, or an external editor.

## Evaluation

1. Resize between roughly 80×24, 120×36, and 160×45.
2. Compare all three variants using `[` and `]`.
3. Press `i` and edit the selected answer. Confirm the cursor is visible, long text wraps, `Ctrl-W` deletes a word, and `Ctrl-J` inserts a newline.
4. Press `Esc` to save and stay, or `Enter` to save and advance.
5. Try `O`, `o`, `D`, `←`, `→`, `gg`, `G`, `Space`, `Backspace`, and `Delete`.
6. Press `?` to inspect the grouped help.

Record the winning structure—or the pieces to combine—in `FINDINGS.md`.
