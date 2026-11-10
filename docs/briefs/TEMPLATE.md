# NNN — Title

Status: Draft
Phase: N (CLAUDE.md §9)
Branch: <type>/NNN-kebab-title
Issue: #N or none

<!-- Status values: Draft | Approved | In progress | Done | Dropped.
     Only the user moves a brief to Approved. No code before that. -->

## What it does

What the user can do once this lands, in 2–5 sentences. Name the commands, keys or config involved.

**Out of scope:** what this item deliberately does not do.

## Concepts

Everything a reader needs to understand to follow this brief. For each concept, give:
- what it is, in plain words;
- why it matters here;
- where to read more (man page, Go doc, spec).

- **Concept name.** Explanation. Read more: …

## Flows

Every path through the feature, step by step. For each step, say which process (client, server or child) and which goroutine does it. Add an ASCII diagram in a code block when it helps.

### Happy path

1. …

### Error paths

- What fails → what the user sees → what state is left behind.

### Shutdown and cleanup

- What is closed, released or killed, and by whom.

## What is needed

- **Packages/files:** new and changed.
- **Types and functions:** the main new ones and where they live.
- **Protocol messages:** new `type`s and their payloads.
- **Config keys:** names, defaults, validation.
- **Dependencies:** new modules and why the stdlib isn't enough (also add a row to CLAUDE.md §7).
- **External tools / system calls.**

## Design

How it works and why this way. The alternatives considered and why they were rejected. Anything here that changes CLAUDE.md.

## Security

The trust boundaries this touches (CLAUDE.md §13) and how each one is handled. Write "None" only with a reason.

## Test plan

- **Unit:** …
- **Integration:** …
- **Manual:** …

## Done when

- [ ] …
- [ ] `go test -race ./...` and `govulncheck ./...` pass
- [ ] Security checks for this item done (CLAUDE.md §13)

## Open questions

## Notes after implementation

Deviations from this brief, and why.

---

<!-- Short brief: only for small changes (a typo, a dependency bump, a one-line
     fix with no new behavior and no trust boundary touched). Replace
     everything above with:

# NNN — Title
Status: Draft
What: one or two lines.
Why: one line.
Security: boundary touched, or "none" with a reason.
-->
