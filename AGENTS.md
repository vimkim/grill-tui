# Agent guidance

## Agent skills

### Issue tracker

Issues and specs are tracked with GitHub Issues. See `docs/agents/issue-tracker.md`.

### Triage labels

The default five-role triage vocabulary is used. See `docs/agents/triage-labels.md`.

### Domain docs

This repository uses a single-context domain-documentation layout. See `docs/agents/domain.md`.

## Build and test

This repository is driven through `just`. Run `just` on its own to list the recipes.

- `just check` runs formatting, `go vet`, and the full test suite. Run it before pushing.
- `just run 12` runs the worksheet from source, with an optional starting number.
- `just build` builds into `.tmp/`, and `just install` installs from the current source tree.

Prefer these recipes over raw `go` commands so every agent and human gates on the same thing.
