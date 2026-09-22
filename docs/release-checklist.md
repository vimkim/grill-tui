# V1 release checklist

Use this checklist for a release candidate after the automated CI job is green. Headless CI
proves formatting, static analysis, the complete automated suite, and an isolated install; the
checks below cover terminal and clipboard behavior that requires real environments.

## Candidate gates

- [ ] Confirm the candidate is a clean checkout of the intended commit with `git status --short`.
- [ ] Run `just check`.
- [ ] Run `go test -race ./...`.
- [ ] Run `just smoke-install` to install into an isolated `GOBIN` and invoke that installed
      command.
- [ ] For an already-created candidate tag, install the public module in a fresh temporary
      `GOBIN` with `go install github.com/vimkim/grill-tui/cmd/grill-tui@<tag>` and run
      `grill-tui config defaults` from that directory.

## Common manual procedure

Use a new temporary working directory in each environment. Run `grill-tui 41`, confirm that a
Worksheet with Answer Slots 41 through 50 opens, press `r`, and then press plain `s`. Paste into
an independent text target and confirm the exact text is `41. recommended`. Quit, reopen in the
same directory, and confirm that the answer and selection resume.

Then verify that a left click selects an Answer Slot and that the wheel scrolls. Verify the
terminal's selection override (commonly Shift-drag) can still select terminal text while mouse
capture is active, and that Worksheet input continues afterward. Record the actual clipboard
backend reported in the status line.

Plain `s` is the required copy path in every environment. Try `Ctrl-S` and record whether it is
delivered, but do not fail the release merely because a terminal, SSH hop, tmux, or flow-control
layer intercepts it.

## Compatibility matrix

- [ ] **WSL under Windows Terminal:** run the common procedure in a Linux distribution and
      confirm `clip.exe` is selected and Unicode/multiline text pastes correctly into a Windows
      application.
- [ ] **Wayland:** with `WAYLAND_DISPLAY` set and `wl-copy` available, confirm `wl-copy` is
      selected and the common procedure succeeds.
- [ ] **X11:** with `DISPLAY` set, confirm the common procedure first with `xclip` available and
      then with only `xsel` available; verify each backend is selected in that order.
- [ ] **SSH:** connect through a terminal that permits OSC 52, remove desktop clipboard tools and
      unset `DISPLAY` and `WAYLAND_DISPLAY`, then confirm plain `s` copies through OSC 52.
- [ ] **tmux:** run the common procedure inside tmux with the client/server configuration intended
      for release testing. Confirm mouse input, terminal text selection, and the selected clipboard
      route pass through correctly.
- [ ] **Direct OSC 52 fallback:** outside tmux, remove or hide `clip.exe`, `wl-copy`, `xclip`, and
      `xsel`; unset desktop display variables; confirm the status reports OSC 52 and plain `s`
      reaches a supporting terminal's clipboard.

Record the terminal, distribution, display server, SSH/tmux versions where applicable, reported
backend, plain-`s` result, mouse result, text-selection result, optional `Ctrl-S` result, and any
required terminal setting. A release passes when the supported route works in each applicable
environment; `Ctrl-S` delivery is informational.

## Functional safety spot checks

- [ ] Press `Ctrl-R` once and confirm the Worksheet remains present. Press another key and confirm
      reset is disarmed. Press `Ctrl-R` twice within two seconds and confirm the primary and backup
      state are removed and the starting-number prompt returns.
- [ ] Set `NO_COLOR=1` and confirm selection, status, and errors remain understandable without
      color.
- [ ] Shrink the terminal until the explicit small-terminal message appears, restore its size, and
      confirm the Worksheet selection is retained.

## Scope check

V1 supports Linux terminals, including Linux under WSL. Native Windows, macOS, AI-service
integration, schema migration, and packaging other than `go install` are not release targets.

The throwaway interaction prototype on `prototype/worksheet-interaction` (`22db4db`) and its
evaluation (`136e94a`) are design evidence only. Do not copy prototype code or its layout-switch
controls into the release.
