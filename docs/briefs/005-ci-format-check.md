# 005 — CI format check

Status: Done

**What:**
- CI's `checks` job fails when `gofmt -l .` lists any file, and prints the files that need formatting.
- CLAUDE.md lists the command.
- Planned item numbers after 004 shift by one (this item took 005), and CLAUDE.md's live references are updated. Finished briefs keep their original numbers as a historical record.

**Why:** CI ran vet and staticcheck but no format check, so an unformatted `internal/proto/fuzz_test.go` was merged in item 003, and fixed in item 004.

**Security:** none. The step adds no permissions, actions or secrets; `gofmt` ships with the Go that `setup-go` already installs.

Approved with the plan for items 005 and 006 (2026-11-16). This is a setup change, committed straight to `main`.
