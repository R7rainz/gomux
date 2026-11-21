# 006 — PTY and process lifecycle

Status: Done
Phase: 2 (CLAUDE.md §9)
Branch: `feat/006-pty-lifecycle`. Feature work, so it gets a PR (CLAUDE.md §14); this brief is the PR's first commit.
Issue: none

Approved through the plan on 2026-11-16, with these decisions:
- killing a pane signals its process group (SIGHUP, then SIGKILL after 2 s, then the PTY is closed);
- a pane closes when its main program exits, and background jobs behave as when a terminal window closes.

## What it does

Adds `internal/term`: run one program on a pseudo-terminal and manage its whole life.
- **Start** the program at a given size.
- **Input:** queue bytes to it. This never blocks the caller.
- **Output:** pass everything it writes to an `io.Writer`.
- **Resize** it.
- **Know when it has ended,** and why (exit code or signal).
- **Stop it** without leaving processes or zombies behind.

Nothing user-visible yet. Item 007 attaches the terminal emulator as the output writer, and item 008 (`gomux new`) puts a real shell in front of you.

**Out of scope:**
- **Choosing the shell or building its environment.** That's item 008, following CLAUDE.md §6.6: login-shell `argv[0]`, `TERM`, and removing the outer terminal's variables.
- **The emulator and query replies:** item 007.
- **The installed-shell test matrix** (sh, dash, bash, zsh, fish): item 007. Until the emulator answers terminal queries, fish waits for a reply at startup.

## Concepts

- **PTY (pseudo-terminal).** A pair of devices.
  - The program sees the **slave** as its terminal (stdin, stdout, stderr).
  - We hold the **master**: reading it gives the program's output, writing it is typing.
  - Read more: `man 4 pty` (macOS), `man 7 pty` (Linux).
- **Session, controlling terminal, process group.**
  - `pty.StartWithSize` starts the program with `setsid()` (a new session it leads) and `TIOCSCTTY` (the slave becomes its controlling terminal).
  - A new session also means a new **process group** whose ID equals the program's PID. `kill(-pid, sig)` signals the whole group: the shell plus anything it started that wasn't moved to another group.
  - Interactive shells put each job in its own group, and on SIGHUP they pass the hang-up on to their jobs.
  - Read more: `man 2 setsid`, `man 2 kill`, `man 4 tty`.
- **SIGHUP and SIGKILL.**
  - SIGHUP means "your terminal went away". It's what closing a terminal window sends. Programs may catch or ignore it to clean up.
  - SIGKILL can't be caught.
  - So we hang up first, give the programs 2 s, and then force it.
- **Window size and SIGWINCH.** `pty.Setsize` (the `TIOCSWINSZ` ioctl) tells the kernel the terminal's size. The kernel then sends SIGWINCH to the foreground process group, which asks for the new size and redraws.
- **Reaping and zombies.** A process that has exited stays a "zombie" in the process table until its parent collects its exit status (`wait`). `cmd.Wait()` does that. A server that forgets to wait leaks zombies.
- **EOF/EIO on the master.** Once every holder of the slave has closed it, reading the master returns end-of-file (macOS) or `EIO` (Linux). Both mean "no more output".
  - A background job can keep the slave open after the main program exits, so we can't wait for EOF to decide that a pane has ended.
- **Blocking file descriptors** (the macOS finding from planning).
  - On Linux, `creack/pty` opens the master through Go's poller, so closing it wakes a goroutine blocked in `Read`.
  - On macOS it opens it in blocking mode, and Go postpones the real `close()` until the blocked `read()` returns.
  - So closing the master can't be relied on to hang up the program or to stop our reader. We signal the process group instead.
- **Close-on-exec.**
  - File descriptors marked `O_CLOEXEC` close automatically when a process runs `exec`. Go marks every descriptor it opens, and `creack/pty` marks the master.
  - So a pane's program inherits only its own terminal (fds 0–2): never another pane's master, nor the server socket.

## Flows

### Start (`term.Start(spec)`)

1. Validate the size: 1 to 65535 columns and rows.
2. Build `exec.Cmd{Path, Args, Dir, Env}` from the spec. `Args[0]` is chosen by the caller: item 008 passes `-zsh` for a login shell.
3. Call `pty.StartWithSize`, which opens the master and slave, sets the size, and forks the child with `setsid`. A missing executable or directory comes back as an error, with nothing left running.
4. Start three goroutines, all owned by the `Term`:
   - **reader:** reads the master into a 32 KB buffer and calls `spec.Output.Write`, until EOF or an error;
   - **writer:** writes queued input to the master;
   - **waiter:** handles exit (see "Exit" below).

### Input (`Write`)

1. Append the bytes to a queue under a lock and signal the writer. `Write` never blocks.
2. If more than 1 MiB is already pending, `Write` returns an error. A program that stops reading its input can only stall itself, not the caller (CLAUDE.md §6.3).

### Resize (`Resize`)

1. `pty.Setsize` with the new size.
2. The kernel sends SIGWINCH to the foreground job, which redraws.

### Exit (the waiter)

1. `cmd.Wait()` returns when the main program exits, and reaps it.
2. Wait up to 200 ms for the reader to reach EOF, so the program's last output still reaches `Output`.
3. Close the master and stop the writer.
4. Close `Done()`. `ExitState()` now reports the exit code or the signal.

If a background job still holds the slave, the pane is done anyway, which is the decision "like closing a window". On Linux, closing the master wakes the reader. On macOS the reader stays blocked until the job exits or writes: `shortcut: a blocked reader goroutine per such orphan; acceptable until it shows up in practice`.

### Kill (`Kill`)

1. If `Done()` is already closed, return. A reaped PID must never be signalled, because its group ID could be reused.
2. `kill(-pid, SIGHUP)`, the same as closing a terminal window. Shells pass it on to their jobs.
3. If `Done()` closes within 2 s, return.
4. Otherwise `kill(-pid, SIGKILL)` and wait for `Done()`.
- PIDs ≤ 1 are refused: `kill(0)` and `kill(-1)` would signal our own group or every process.
- Concurrent and repeated calls are safe.
- Server shutdown (item 008) will `Kill` every pane in parallel.

### Error paths

| Situation | Result |
|---|---|
| Executable or directory missing | `Start` returns the exec error; no goroutines, no open PTY |
| Size 0 or > 65535 | `Start` returns an error |
| `Write` after exit | Returns an error |
| `Write` with more than 1 MiB pending | Returns an error |
| `Resize` after exit | Returns an error |
| `Output.Write` returns an error | The reader keeps draining the PTY anyway, because the program must never block on its output; the error is dropped |

### Shutdown and cleanup

Every `Term` ends with:
- the process reaped;
- the master closed;
- the writer stopped;
- the reader either finished or blocked on an orphan (macOS, see above).

## What is needed

- **Files:** `internal/term/term.go` and `internal/term/term_test.go`.
- **Types and functions:**
  - `term.Spec{Path, Args, Dir, Env, Cols, Rows, Output}`
  - `term.Start(Spec) (*Term, error)`
  - `(*Term) Pid() int`
  - `(*Term) Write([]byte) (int, error)`
  - `(*Term) Resize(cols, rows int) error`
  - `(*Term) Kill()`
  - `(*Term) Done() <-chan struct{}`
  - `(*Term) ExitState() *os.ProcessState`
- **Protocol messages and config keys:** none.
- **Dependencies:** `github.com/creack/pty` v1.1.24, already chosen in CLAUDE.md §7.

## Design

- **`Output` is an `io.Writer`.** It's the standard interface the emulator already implements (`SafeEmulator.Write`), so item 007 plugs in without a new type.
- **The input queue is capped (1 MiB)**, not unbounded and not blocking. The event loop calls `Write` and must never block (CLAUDE.md §6.3), and the queue mustn't grow without limit. A cap with an error is the honest middle.
- **Kill by process-group signal,** decided at approval. It works identically on Linux and macOS, and it's what tmux does. Closing the master stays as the final step, after the process is gone.
- **`Done` doesn't wait for orphans,** decided at approval. The main program's exit plus a 200 ms drain defines the end of a pane.
- **The 2 s grace period and the 200 ms drain** are package variables, so tests can shorten the grace. They aren't config keys: neither is a user preference.
- **Rejected:**
  - closing the master to hang up (unreliable on macOS);
  - SIGKILLing the group whenever the main program exits (only partial, and it changes the "like a window" semantics);
  - an unbounded input queue;
  - a channel of byte slices for input (it blocks once full).

## Security

- **Pane output is untrusted** (CLAUDE.md §13). This package only moves bytes to `Output`: it doesn't interpret them and never logs them.
- **Signals:**
  - only to the child's own process group, and never after it has been reaped;
  - PIDs ≤ 1 refused;
  - the PID comes from `os/exec`, not from outside input.
- **No descriptor leaks:** a test checks that a pane's program sees only fds 0–3. That means no other pane's master and no server socket.
- **Environment and executable** come from the caller and are trusted (`$SHELL`, config). They are never logged.
- **`/security-review`** runs before the PR.

## Test plan

All tests use `/bin/sh` scripts, so they are deterministic on Linux and macOS.

| Test | Asserts |
|---|---|
| `printf hello; exit 3` | `Output` has `hello`; `Done` closes; exit code 3 |
| `read x; printf got-%s "$x"` + `Write("abc\n")` | `Output` has `got-abc` |
| `stty size; read _; stty size` + `Resize(100, 30)` | `24 80`, then `30 100` |
| `sleep 100` + `Kill` | `Done` in < 1 s; ended by SIGHUP |
| `trap "" HUP; while :; do sleep 1; done` + `Kill` (grace 300 ms) | ended by SIGKILL after the grace; `kill(-pid, 0)` → ESRCH |
| `(trap "" HUP; sleep 3) & echo started` | `Done` in < 1 s, without waiting for the orphan |
| after `Done` | `wait4(pid, WNOHANG)` → ECHILD (no zombie) |
| a second `Term` runs `ls /dev/fd` while the first is open | only fds 0–3 |
| missing executable; size 0 | `Start` errors |
| `Kill` after exit; two concurrent `Kill`s | no-op; race-free |
| `Write` over the cap (`sleep 100` never reads; small cap) | error |

Run with `go test -race -count=5 ./internal/term/`.

## Done when

- [x] `internal/term` with the API above and every test passing under `go test -race -count=5`
- [x] Full suite, gofmt, vet (incl. `GOOS=linux`), staticcheck and govulncheck clean
- [x] CLAUDE.md updated:
  - §6.6: killing a pane and server shutdown by group signal; pane exit;
  - §6.3: `Term` goroutines and the input cap;
  - §7: creack/pty note;
  - §2;
  - §9
- [x] `/security-review` run on the branch, with no unresolved findings
- [x] PR opened with a Security line; CI runs on ubuntu and macOS on the PR

## Open questions

None.

## Notes after implementation

**The macOS assumption, measured.** A one-off probe (not committed) closed the master of a running `sleep 100` while the reader was blocked. The program was *not* hung up within 2 s. That confirms the planning finding that closing the master can't replace signalling the group on macOS (CLAUDE.md §6.6).

**Leak test rewritten.**
- **Before:** the first version listed `/dev/fd` with `ls` and saw fds 3 and 4. Those were `ls`'s own handles, not leaks; `lsof` on the shell showed only fds 0–2, all its own terminal.
- **After:** the test asks the shell itself, using the builtin `[ -e /dev/fd/N ]` for 3–20.
- **Self-check:** it first opens fd 9 on purpose and requires the probe to see it, so it can't pass vacuously.

**Security review: no security findings.** It covered:
- signalling the wrong group (the PID ≤ 1 check and the `setsid` group);
- descriptor leaks (both PTY ends and the server socket are close-on-exec);
- argument injection (an explicit argument list, no shell);
- untrusted output, which is never parsed or logged.

**Bug found by the review, fixed in this PR.**
- **What:** `gomux server` (item 004) called `signal.Ignore(SIGHUP)`. An ignored signal stays ignored in every program a process starts.
- **Impact:** once panes run inside the server (item 008), every pane program would ignore `Term.Kill`'s hang-up. Panes would be SIGKILLed only after the grace period, and background jobs in their own process groups would survive pane kill and server shutdown.
- **Proof:** a probe here showed `Kill` ending `sleep` by SIGKILL with `signal.Ignore` in the parent, and by SIGHUP with `signal.Notify`.
- **Fix:** commit `fix(cmd): handle sighup instead of ignoring it`, plus `TestHangupsHandledNotIgnored`. The test was shown to fail on the old code ("child survived its own SIGHUP").
- **Scope:** this is outside the approved brief. It was raised with the user after the review, and they said to continue with the recommended path, fixing it in this PR. It is recorded in CLAUDE.md §6.6.

**Code review (`/code-review high`) before merging, at the user's request to double-check everything.** It raised 7 findings. Each was checked against the code, and all are resolved in commit `fix(term): sweep the whole process group on kill`.

1. **Kill stopped too early (important).**
   - **The bug:** once SIGHUP killed the main program, `Kill` returned without SIGKILL. Group members that ignore SIGHUP survived pane kill and server shutdown.
   - **The fix:** `Kill` now waits up to 2 s for the *whole group* to empty, then SIGKILLs what's left.
   - **The test:** `TestKillSweepsGroupAfterMainDies`. The bug was reproduced against the old code: "old Kill left the HUP-immune subshell running".
   - **What I didn't take:** the review also suggested sweeping when `Kill` is called after `Done`. Rejected, because the pane has ended, its leftover jobs follow the "like closing a window" decision, and its group ID may belong to an unrelated group after PID wraparound. `TestKillAfterDoneSignalsNothing` documents this.
2. **The "can't interleave" claim was wrong.** `reaped` was set only after `wait4` had reaped the process. The flag is gone. Signals now go to the group only while the pane is live or the group still has members, and a group ID can't be reused while any member is alive (CLAUDE.md §6.6).
3. **Output after `Done`.** The reader now discards output read after `Done`, so a torn-down pane's emulator never gets writes. Test: `TestNoOutputAfterDone`.
4. **The input cap ignored in-flight bytes**, so the real cap was about 2 MiB. Now the queued and in-flight bytes are counted together. Also, a failed PTY write now marks the `Term` finished, so later `Write`s error instead of disappearing. `TestInputCap` covers the cap.
5. **A paste over 1 MiB was always refused.** A write into an idle queue is now accepted whatever its size (`TestInputCap`).
6. **An unsignallable process could hang `Kill`** (e.g. a `sudo` child, EPERM). The final wait is now bounded by a further `killGrace`.
7. **The macOS writer goroutine could also stay parked.** Documented in the shortcut comment next to the reader's.

**Verification:**
- `go test -race -count=10 ./internal/term/` and the full race suite pass.
- gofmt, `go vet` (darwin and linux), staticcheck v0.8.2 and govulncheck v1.9.0 ("No vulnerabilities found") are clean.
