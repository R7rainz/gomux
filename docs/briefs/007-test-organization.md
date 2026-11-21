# 007 — Test organization and shared helpers

Status: Done
Phase: 2, housekeeping before the screen-state item (CLAUDE.md §9)
Branch: `test/007-test-organization`. It changes code (tests), so it gets a PR (CLAUDE.md §14); this brief is the PR's first commit.
Issue: none

Approved 2026-11-21 ("move on", after the user asked for properly organized tests); merge approved explicitly 2026-11-22 ("lets merge it").

## What it does

Makes the test suite consistent and easier to extend. No production code changes, and no test loses or weakens an assertion.

1. **A shared test-only package, `internal/testutil`,** replaces the copies of the same helpers spread across packages:
   - `SocketDir(t)`: a short, auto-removed directory under `/tmp` for Unix sockets. macOS limits socket paths to 104 bytes, which `t.TempDir()` exceeds. Today four test files each have their own copy (`sock`, `server`, `client`, `cmd/gomux`).
   - `Eventually(t, timeout, what, cond)`: polls a condition until a deadline and fails with `what`. It replaces the separate polling loops in `term` and `cmd/gomux`.
   - `Buffer`: a goroutine-safe `bytes.Buffer`, for capturing output that one goroutine writes while the test reads it. It's in `term` today, and the screen-state item needs it too.
2. **No fixed sleeps used as synchronization.** `TestKillForcesStubbornGroup` sleeps 100 ms "to let the trap take effect". The script will print `ready` after installing the trap, and the test will wait for that. A fixed sleep is a flaky test waiting for a slow CI machine.
3. **Written test conventions,** in a new CLAUDE.md section, "Testing", so every future item follows the same layout (see Design).
4. **Planned items are referred to by name** in CLAUDE.md ("the screen-state item", "the `gomux new` item"), and numbers are given only when a brief is written. This ends the renumbering we did twice (for 005 and now 007).

**Out of scope:** new features and new test coverage. The shell matrix arrives with the screen-state item.

## Concepts

- **Test files live next to the code they test.** `foo.go` is tested by `foo_test.go` in the same package, so tests can reach unexported names. Go compiles `_test.go` files only into test binaries. Read more: `go help test`.
- **A test-only helper package.** A normal package (`internal/testutil`) that only `_test.go` files import. Because no production code imports it, it never ends up in the `gomux` binary. A test enforces that with `go list -deps`.
- **`t.Helper()` and `t.Cleanup()`.** `Helper` makes failures point at the calling test's line instead of the helper's. `Cleanup` runs teardown when the test ends, even if it fails. Read more: `go doc testing.T`.
- **Polling instead of sleeping.** Asynchronous results (a goroutine's output, a socket appearing) are checked repeatedly until a deadline, instead of sleeping a fixed time and hoping. That makes tests fast when the machine is fast and still correct when it is slow.

## Flows

Not applicable: this item has no runtime behavior. The working flow:

1. Record the current pass list (`go test -race -count=1 -v ./... | grep -c '^--- PASS'`) as the baseline.
2. Add `internal/testutil` with its own tests.
3. Switch each package's tests to it, one commit per package. Each commit builds and passes.
4. Run the pass list again: the same tests pass under the same names, with no test removed.

## What is needed

- **New files:** `internal/testutil/testutil.go` and `testutil_test.go`.
- **Changed:** `internal/sock/sock_test.go`, `internal/server/server_test.go`, `internal/client/conn_test.go`, `internal/term/term_test.go`, `cmd/gomux/main_test.go`, and `CLAUDE.md` (the new "Testing" section and the item references). The merge rule already landed in item 006's PR.
- **Dependencies, protocol messages, config keys:** none.

## Design

**The test conventions, which become CLAUDE.md "Testing":**

| Rule | Why |
|---|---|
| Unit tests next to the code, same package (`x_test.go`) | Go convention; can test unexported behavior |
| End-to-end tests only in `cmd/gomux`, against the real binary built with `-race` | Catches wiring and race bugs that package tests miss |
| Fuzz tests in `fuzz_test.go`, seeds via `f.Add` | One place to find them; CI replays the seeds |
| Shared helpers only in `internal/testutil`; production code never imports it (enforced by a test) | No copies; nothing test-only in the binary |
| Table-driven once there are 3 or more similar cases | Easy to add a case |
| Name tests after the behavior (`TestKillForcesStubbornGroup`), not the function | The failure message says what broke |
| No fixed sleeps for synchronization: `testutil.Eventually`, or a marker the program prints | No flakes on slow CI |
| Programs started by tests are `/bin/sh` scripts, except where the test's purpose is a real shell (the shell matrix) | Deterministic on every machine |
| Sockets under `testutil.SocketDir` | The macOS 104-byte path limit |
| Every bug fix adds a regression test, shown to fail without the fix | Proves the test guards the bug |
| Concurrency code is also run with `-race -count=5` before its PR | Flushes out intermittent races |

**Rejected:**
- `testify`: CLAUDE.md §7 says no; the stdlib is enough.
- Separate `_integration` build tags: the whole suite runs in about 10 s, so there's nothing to skip yet.
- Moving tests into `x_test` external packages: they'd lose access to the unexported names that the current tests rely on.

## Security

None. The item changes tests and documentation only, and touches no runtime trust boundary. The one security-relevant test, the descriptor-leak check, keeps its assertions and self-check unchanged.

## Test plan

- `internal/testutil` gets its own tests:
  - `SocketDir` returns a path short enough for a socket, and the directory is gone after the test;
  - `Eventually` passes once the condition holds;
  - `Buffer` is race-free under `-race`.
- **Same coverage:** the passing-test count after the refactor is the baseline count plus the new `testutil` tests. Every original test name still passes.
- `go test -race -count=5 ./internal/term/ ./internal/server/`: no flakes with the sleep removed.
- gofmt, vet (darwin and linux), staticcheck and govulncheck are clean, and CI is green.
- A test runs `go list -deps ./cmd/gomux` and confirms `internal/testutil` is not linked into the binary.

## Done when

- [x] `internal/testutil` with tests; the four duplicated socket-dir helpers, the polling loops and the term buffer are replaced
- [x] `TestKillForcesStubbornGroup` waits for a `ready` marker instead of sleeping
- [x] Same tests pass as before (names compared), plus the new ones
- [x] CLAUDE.md: "Testing" section, planned items referred to by name
- [x] Code review of the diff clean; CI green; merged with Rebase and merge (after the user's explicit approval, see Notes)

## Open questions

None.

## Notes after implementation

**No test lost.**
- The passing-test names were recorded before the refactor: 76, including subtests.
- After it: 81 = the same 76 plus 5 new `testutil` tests (4 tests and 1 subtest). `comm` showed no name disappearing, and nothing fails or skips.

**The ready marker.** The script prints `trapped` rather than `ready`, which says what it marks. The idea is the same as in the brief.

**Observation windows.** Two fixed sleeps remain, both labelled in comments:
- `TestNoOutputAfterDone` (300 ms) and `TestHangupsHandledNotIgnored` (50 ms) each prove that something does *not* happen, so they must watch for a while.
- CLAUDE.md §15 spells out this exception.

**Code review (`/code-review high`), 4 findings, all handled:**
1. **A nil-dereference panic, fixed.** `TestSocketDirIsShortPrivateAndRemoved` read `fi.Mode()` in its failure message even when `os.Stat` had failed. Grepping for the same pattern turned up two older `fi, _ := os.Stat(...)` lines in `sock_test.go` (from item 004), also fixed. All three now check the error first. Commit `test: check stat errors before reading file modes`.
2. **The brief wasn't closed.** It is now.
3. **§12's naming examples** still used "007 — Layout tree". They now use the finished item 006, so they can't go stale.
4. **Approval wording.** This brief was approved with the user's "move on" in reply to "Reply approved…". The review rightly notes that §12 wants an explicit approval, so the merge waited for the user's explicit OK, given on 2026-11-22.

**A flaky test from item 006, caught by this PR's macOS CI and fixed.**
- **What failed:** `TestKillAfterDoneSignalsNothing` timed out waiting for the background subshell's `immune-ready` output.
- **The race:** the pane's main command (`echo started`) exited at once, the pane ended about 200 ms later, and output after `Done` is discarded by design. On a slow runner the marker came too late.
- **Reproduced locally:** under CPU load (12× `yes`), 7 of 40 runs failed.
- **The fix:** readiness is now a flag file the subshell creates after installing its trap, and the main command also waits for it (commit `test(term): fix a race in the hup-immune helper`).
- **Result:** 100 runs under the same load pass, and so do 10 loaded runs of every concurrent package, including the end-to-end tests.
- **Recorded in CLAUDE.md §15** as two conventions:
  - signal readiness with a flag file, not output the code under test may drop;
  - run concurrency tests under CPU load before a PR.

**Verification:**
- gofmt, `go vet` (darwin and linux), staticcheck and govulncheck are clean.
- `go test -race -count=5` passes on `term`, `server`, `client`, `sock` and `testutil`, and 10 runs pass under CPU load.
