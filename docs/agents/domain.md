# Domain Docs

Before exploring the codebase, read the root `CONTEXT.md` and relevant ADRs under `docs/adr/`. If these files do not exist, proceed silently; domain-modeling skills create them when needed.

## Layout

This is a single-context repository:

```
/
├── CONTEXT.md
├── docs/adr/
└── src/
```

Use terminology defined in `CONTEXT.md`. If work contradicts an existing ADR, identify the conflict explicitly instead of silently overriding it.
