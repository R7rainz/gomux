# 008 — Server-side screen state

Status: Done
Phase: 2 (CLAUDE.md §9)
Branch: `feat/008-screen-state`. Feature work, so it gets a PR (CLAUDE.md §14); this brief is the PR's first commit.
Issue: none

Approved 2026-11-25 ("approved"), with the decisions in Open questions.

## What it does

Gives every pane a screen. Item 006 runs a program on a PTY and hands over its raw output bytes. This item feeds those bytes into the terminal emulator chosen by the prototype (`x/vt`). The server then always knows exactly what each pane looks like: every character, its colors and style, and the cursor.

A new type, `term.Pane`, ties together one program (a `term.Term`) and one emulator, and lets the server:
- **read the screen** as a snapshot: lines of styled text plus the cursor. This is what clients will draw (CLAUDE.md §6.7);
- **send input as events**: key presses and pastes, encoded by the emulator according to the modes the program turned on (application cursor keys, bracketed paste);
- **resize** the emulator and the PTY together;
- **answer the program's questions** automatically ("what terminal are you?", "where is the cursor?", "what's the background color?"). Without the answers, fish stalls at startup and nvim guesses wrong.

It also adds **the installed-shell test matrix** (CLAUDE.md §8): sh, dash, bash, zsh, fish and nushell each start as a login shell in a pane, run a typed command, and exit. CI installs the shells the runners lack, so none of them can be skipped there.

Nothing is user-visible yet. The `gomux new` item sends these snapshots to a client and draws them.

**Out of scope:**
- **Protocol messages and the client.** The snapshot *types* are defined here (see Design, decision 1), but no message carries them yet. That, and drawing them, is the `gomux new` item.
- **Telling the event loop a pane changed** ("dirty" notification) and the 60 Hz render tick: they need the event loop to consume them, so they also come with the `gomux new` item.
- **Mouse events.** Clients send them from the `gomux new` item on; `SendMouse` is added then.
- **Scrollback viewing, copy mode, titles, OSC 8 links:** later phases (CLAUDE.md §5, §9). `x/vt` already keeps 10,000 lines of scrollback per pane, matching the planned `general.scrollback` default, so memory stays bounded.
- **Choosing the user's shell and building its environment** (CLAUDE.md §6.6): the `gomux new` item. The matrix test passes its own.

## Concepts

- **Terminal emulator (screen model).** A state machine that reads a program's output, including escape sequences, and keeps a grid of cells. Each cell holds one character (a grapheme cluster) and its style. The emulator also tracks the cursor and modes, and has a second grid, the alternate screen, which full-screen apps like vim use. gomux runs one emulator per pane, in the server, so a reattaching client gets the current screen and several clients see the same one. Read more: CLAUDE.md §6.7, brief 002.
- **Query replies.** Programs ask the terminal questions by printing escape sequences: Primary Device Attributes (DA1, `ESC [ c`), the cursor position (`ESC [ 6n`), the background color (`OSC 11 ; ?`). The emulator answers by writing bytes back to the program's input. fish 4 waits for the DA1 answer at startup, and nvim picks its colors from the OSC 11 answer. Read more: "XTerm Control Sequences" (invisible-island.net/xterm/ctlseqs).
- **`io.Pipe` is synchronous.** `x/vt` writes replies and encoded keys into an `io.Pipe`. A write into a pipe blocks until someone reads it, and `x/vt` writes while it is in the middle of processing output. So one goroutine per pane must always be reading that pipe, and must never block itself. Read more: `go doc io.Pipe`.
- **Why a stuck program is dangerous.** If a program stops reading its input but keeps asking questions, its PTY input buffer fills (a few KB). Writing the replies straight to the PTY would then block the pipe reader, and with it the emulator and every screen read. CLAUDE.md §6.3 records this caveat from the prototype. Item 006's `Term.Write` already queues input without blocking, with a 1 MiB cap, so the replies go through it.
- **Input modes.** The same key means different bytes depending on what the program asked for. Up-arrow is `ESC [ A` normally and `ESC O A` in *application cursor mode* (vim, less). A paste is wrapped in `ESC [200~ … ESC [201~` only in *bracketed paste mode*, which stops a shell from running a pasted block line by line. Only the emulator knows the modes, because the program turns them on by printing to it. That is why input travels as events and is encoded next to the emulator (CLAUDE.md §6.7).
- **Style runs.** A screen row is sent as runs, i.e. consecutive cells with an identical style merged into one piece of text. An 80-column prompt line becomes 3–5 runs instead of 80 cells. Trailing blank cells are left out.
- **Wide characters.** CJK characters and most emoji take two cells. `x/vt` stores the character in the first cell and an empty placeholder in the second. A run carries the character once; whoever draws it gives it two columns. Read more: Unicode UAX #11.
- **Login shells.** A shell started with a `-` in front of its name (`argv[0]` = `-zsh`) reads the login profile, which is how PATH gets set up on macOS. tmux starts shells this way, and gomux will too (CLAUDE.md §6.6). Read more: `man bash`, section INVOCATION.

## Flows

```
                    ┌────────────────────── term.Pane ───────────────────────┐
 program ──PTY──▶ Term reader ──▶ [mu] vt.Emulator ──replies, keys──▶ io.Pipe │
                    │                   ▲          (encoded)             │    │
 server: SendKey ───┼──────────▶ [mu] ──┘                                │    │
 server: Paste      │                                                    ▼    │
 server: Snapshot ──┼──▶ [mu] cells → runs                       reply drain   │
                    │                                                    │    │
 program ◀──PTY── Term writer ◀── Term input queue (≤ 1 MiB) ◀──────────┘    │
                    └──────────────────────────────────────────────────────┘
```

`[mu]` is the pane's one mutex around the emulator.

### Start

1. **The caller** (the server, from the `gomux new` item) calls `term.StartPane(spec)`. The spec is item 006's `term.Spec`, with `Output` left empty, because the pane's emulator receives the output.
2. The size is checked (1–65535, as in item 006), and an `x/vt` emulator is created at that size.
3. `term.Start` runs the program, with the emulator as its output. Errors (missing executable, bad directory) are returned as they are today.
4. Two goroutines start: the **reply drain**, and a small **closer** that waits for `Done()`.

### Output: program → screen

1. **Term's reader goroutine** reads the PTY and writes the bytes to the pane. The pane locks its mutex and calls `emulator.Write`.
2. The emulator updates its cells. If the bytes held a query, it writes the answer into its pipe. That pipe write waits for the reply drain, which is always reading.
3. **The reply drain goroutine** reads the answer and hands it to `Term.Write`, which queues it and returns at once. **Term's writer goroutine** delivers it to the program.
4. If the program has stopped reading its input and the queue is full, `Term.Write` fails and the drain drops that reply. The emulator never waits on a stuck program, so neither does anything else that needs the pane.

### Input: server → program

1. The server calls `pane.SendKey(key)` or `pane.Paste(text)`. These lock the mutex and ask the emulator to encode the event for the program's current modes.
2. The encoded bytes go into the same pipe and follow steps 3–4 above.

### Snapshot

1. The server calls `pane.Snapshot()`. It locks the mutex, so the whole screen comes from one moment and is never half-updated.
2. For each row it walks the cells: wide-character placeholders are skipped, neighbours with the same style are merged into runs, and the trailing blank cells are dropped. It also reads the cursor's position, visibility, shape and blink.
3. It returns a `proto.Screen` (Design, decision 1). Calling it costs roughly one pass over the cells (2,000 for 80×24).

### Resize

1. `pane.Resize(cols, rows)` checks the size, resizes the emulator under the mutex, then calls `Term.Resize`. The kernel sends SIGWINCH to the program, which redraws at the new size.
2. Emulator first, PTY second: by the time the program redraws, the emulator already has the new size.
3. An invalid size is refused before anything changes. After the program has exited, `Resize` returns `ErrExited`, as today.

### Error paths

- **Bad spec** (size, a non-empty `Output`, a missing executable or directory) → `StartPane` returns an error and nothing is left running.
- **The program stops reading its input** → replies and keys queue up to 1 MiB, then they are dropped. The pane's screen, its snapshots and every other pane keep working, and `Kill` still stops it. A test proves this (Test plan).
- **A hostile program asks for its title or the clipboard back** (`CSI 21 t`, `OSC 52 ; ?`) → no answer. `x/vt` doesn't implement these reports. A test pins this, so a library upgrade can't silently change it (Security).

### Exit, shutdown and cleanup

- **The program exits:** item 006's sequence runs (reap, drain the last output, close `Done()`). Then the closer goroutine closes the emulator's pipe, the reply drain reads end-of-file and returns, and both goroutines are gone.
- **After `Done()`:** `Snapshot` still returns the last screen. `SendKey` and `Paste` do nothing, because a write into the closed pipe fails at once. `Resize` returns `ErrExited`.
- **`Kill`, `Done`, `ExitState`, `Pid`** pass straight through to the `Term`, with the semantics from item 006 (CLAUDE.md §6.6).
- **The emulator's own `Close` is never called.** In `x/vt` v0.1.0 it races with a concurrent `Read` (CLAUDE.md §6.3), so the closer closes the pipe instead, as the prototype found.

## What is needed

- **New files:**
  - `internal/proto/screen.go`: the snapshot types.
  - `internal/term/pane.go`: `Pane`, the reply drain, and the conversion from cells to runs.
  - `internal/term/pane_test.go`: the pane and snapshot tests.
  - `internal/term/shells_test.go`: the shell matrix.
- **Changed:** `go.mod` and `go.sum`, `.github/workflows/ci.yml`, and `CLAUDE.md`.
- **Types and functions:**

  ```go
  // internal/term
  func StartPane(s Spec) (*Pane, error) // s.Output must be nil
  func (p *Pane) SendKey(k vt.KeyPressEvent)
  func (p *Pane) Paste(text string)
  func (p *Pane) Resize(cols, rows int) error
  func (p *Pane) Snapshot() proto.Screen
  func (p *Pane) Kill()
  func (p *Pane) Done() <-chan struct{}
  func (p *Pane) ExitState() *os.ProcessState
  func (p *Pane) Pid() int

  // internal/proto
  type Screen struct { Cols, Rows int; Lines []Line; Cursor Cursor }
  type Line []Run                  // trailing blank cells omitted
  type Run struct { Text string; Style Style }
  type Style struct { Fg, Bg, UnderlineColor Color; Attrs Attr; Underline Underline }
  type Color string                // "" = default, "0".."255" = palette, "#rrggbb" = truecolor
  type Attr uint8                  // AttrBold, AttrFaint, AttrItalic, AttrBlink, AttrReverse, AttrConceal, AttrStrikethrough
  type Underline uint8             // none, single, double, curly, dotted, dashed
  type Cursor struct { X, Y int; Hidden bool; Shape CursorShape; Blink bool }
  ```

  JSON tags follow `proto.go` (snake_case, empty fields omitted). The `Attr`, `Underline` and `CursorShape` values are gomux's own constants, mapped from `x/vt`'s. A library upgrade then can't change what's on the wire.
- **Protocol messages:** none yet. The types above become payloads in the `gomux new` item. `proto.Version` stays 1.
- **Config keys:** none.
- **Dependencies:** `github.com/charmbracelet/x/vt` v0.1.0, already chosen by the prototype (CLAUDE.md §7). It brings in `ultraviolet` and `x/ansi` (shared with Bubble Tea v2, which the `gomux new` item adds) and a handful of small indirect modules. All of them are pure Go, so `CGO_ENABLED=0` still holds. `govulncheck` checks them.
- **CI** (`.github/workflows/ci.yml`, in the `checks` job before the tests):
  - ubuntu: `sudo apt-get install -y zsh fish` (sh, dash and bash are already there);
  - macOS: `brew install fish nushell` (sh, dash, bash and zsh ship with macOS);
  - each job sets `GOMUX_REQUIRE_SHELLS` to the shells it installed or ships with, so the list of required shells lives next to the install step;
  - nothing needs new permissions, actions or secrets.

## Design

**`Pane` composes a `Term` and a plain `vt.Emulator` behind one mutex.**
- Every emulator call goes through that mutex: output from the reader, `SendKey`, `Paste`, `Resize`, `Snapshot`.
- The pipe reads don't take the mutex. `io.Pipe` is safe across goroutines, and the drain must keep reading while the emulator writes under the lock.
- **Why there is no deadlock:** the emulator's pipe write waits only on the drain, and the drain waits only on `Term.Write`'s own short lock, never on the pane's mutex.
- **Rejected: `vt.SafeEmulator`.** It locks each call separately, so a snapshot made of 2,000 `CellAt` calls could catch a line halfway through an update, and would take 2,000 locks to read. Its `Read` also checks the unsynchronized `closed` flag from the race in CLAUDE.md §6.3.

**Replies go through `Term.Write`.** It is exactly the "drain into a buffer and write from a separate goroutine" that CLAUDE.md §6.3 asks for: a queue capped at 1 MiB, written by Term's writer goroutine. No second buffer is needed.

**The shell matrix:**
- **Which shells:** sh, dash, bash, zsh, fish and nushell (`nu`) run whenever they are installed, and are skipped otherwise. A shell named in `GOMUX_REQUIRE_SHELLS` must be present, or the test fails.
  - CI sets it to `sh dash bash zsh fish` on ubuntu and adds `nu` on macOS, so a broken install step can't silently shrink the matrix.
  - nushell runs on macOS CI only, installed with Homebrew (decided at review, rather than dropping it from §8). One runner is enough to verify its probe.
- **Each shell runs as a login shell** (`argv[0]` = `-fish`) with a fresh temporary `HOME`. That makes the test about gomux, not about whoever's dotfiles are on the machine, and CI has none anyway. An empty `.zshrc` stops zsh's first-run wizard from taking over the keyboard. The environment is minimal: `HOME`, `PATH`, `TERM=xterm-256color`, and the locale variables if set.
- **The probe, typed as key events:**
  1. Wait for the prompt (any non-blank screen).
  2. Type `echo AB""CD` and Enter, and wait for a screen line that is exactly `ABCD`. The typed command itself shows `AB""CD`, so only real output can match. That proves the shell received the keys, ran the command and drew its output. nushell's syntax differs, so it gets `echo ('AB' + 'CD')`, which has the same property.
  3. Check the screen has no "Device Attribute" warning (fish prints one if nobody answers its startup query).
  4. Type `exit 7` and Enter, and check the exit code is 7.
- **Your real dotfiles get tried by hand** in the `gomux new` item, the first time you can open gomux yourself.
- **Where it lives:** `internal/term`. The pane is what runs shells, and there is no binary path to a pane yet. CLAUDE.md §15 already allows real shells for exactly this test.

**Decision 1: the snapshot types live in `internal/proto`** (decided at review).
- They are the wire format, so they belong with the other message types, and the `gomux new` item sends them as they are.
- `term` gains an import of `proto`, which is pure and imports nothing internal, so no cycle is possible. CLAUDE.md §6.2's dependency line gets `term → proto`.
- **Rejected: keeping them in `term`.** The `gomux new` item would then need a second, identical set of types in `proto` and a conversion between them.

**Rejected: sending each line as an ANSI-styled string** (`x/vt`'s `Render`). It would be less code, since Bubble Tea can draw such strings. But:
- the client would put escape sequences derived from pane output straight onto your terminal;
- the client couldn't restyle single cells, which copy-mode selection and search highlighting need;
- CLAUDE.md §6.7 already chose style runs.

**Colors keep their kind.**
- Palette colors stay palette indexes (`"1"`, `"200"`), so your terminal's theme still applies to them.
- Only truecolor is sent as `#rrggbb`.
- Default colors are `""`, drawn in your terminal's own defaults.

**Known limit: the emulator assumes a black background.** `x/vt` answers OSC 10/11 with white on black, so nvim and others pick dark-background colors even in a light terminal. The fix is for the client to tell the server your terminal's real colors, which belongs to the client work in the `gomux new` item. It's recorded in CLAUDE.md §6.6.

**CLAUDE.md changes:**
- §2 (current state).
- §6.2: `term → proto`.
- §6.3: the reply drain as built, and why there is one mutex and no `SafeEmulator`.
- §6.4: a Pane's `Term` field is a `term.Pane`.
- §6.6: the known OSC 10/11 limit.
- §7: the `x/vt` row notes it was added in item 008.
- §8: the matrix as built, what CI installs, and that nushell runs on macOS CI only.
- §9: Phase 2 progress.

## Security

**Pane output is untrusted** (CLAUDE.md §13). This item is where it first gets interpreted:
- **Replies never echo text the program chose.** Reading `x/vt`'s source, every reply it can write is numeric or fixed:
  - device attributes (DA1, DA2), status and cursor-position reports, mode reports;
  - OSC 10/11/12 colors;
  - in-band resize, focus events.
  - There are no title reports, no clipboard reads, and no DCS replies (DECRQSS, XTGETTCAP), which are the classic ways to make a terminal type attacker-chosen text into a shell.
  - **A test pins this through the real path** (program → PTY → emulator → reply → PTY → program): a script asks for its title back and for the clipboard, then sends a DA1 query and reads its input. It must get the DA1 answer and nothing else.
- **A stuck or hostile program can't freeze the server.** Its replies are capped and dropped (Flows, error paths), and a test floods queries from a program that never reads its input.
- **Memory is bounded:**
  - the screen is fixed-size;
  - scrollback is 10,000 lines;
  - the escape-sequence parser caps a single sequence's data at 4 MiB;
  - the input queue is capped at 1 MiB.
- **Titles aren't shown anywhere yet.** CLAUDE.md §13's title cap and control-character stripping apply when the status bar shows them.
- **Snapshot text:** cell contents are printable graphemes, because the emulator consumes control characters. The client still strips control characters before drawing (the `gomux new` item), as defence in depth.
- **Nothing is logged.** No pane input, output or environment.
- **CI installs shells from the distro and Homebrew repositories.** They run only in the throwaway runner, never ship in gomux, and the `checks` job has no secrets.
- **`/security-review` before the PR,** since this item touches the pane-output trust boundary. Plus `govulncheck` with the new dependencies.

## Test plan

**Snapshots** (`pane_test.go`, writing bytes straight into an emulator, with no PTY, so they are fast and exact). Table-driven:
- plain text, with trailing blanks dropped;
- truecolor, basic and 256-color palette foregrounds, and a background;
- bold + italic, reverse, curly underline with an underline color;
- `日本` and `👍🏽`: one run each, with the placeholders skipped;
- neighbouring cells with the same style merged into one run, and a style change splitting it;
- the cursor's position; hidden (`ESC [?25l`); bar shape, blinking (`ESC [5 q`).

**Pane** (`pane_test.go`, `/bin/sh` scripts on a real PTY):
- **Output reaches the screen:** `printf` text shows up in `Snapshot`.
- **Queries are answered, hostile ones aren't:** the script from Security; the DA1 answer arrives, and neither the title nor `52;` does.
- **Keys follow the program's modes:**
  - the script turns on application cursor mode, prints `ready`, then hex-dumps its input. Up-arrow must arrive as `1b 4f 41`;
  - the same with bracketed paste: `Paste("x")` must arrive wrapped in `ESC [200~ … ESC [201~`.
- **Resize:** `stty size` before and after; the snapshot's size changes; an invalid size is refused and changes nothing.
- **A stuck program doesn't stall the screen.** Regression test for CLAUDE.md §6.3, shown to fail when replies are written straight to the PTY.
  - The program floods cursor-position queries and never reads its input. The test lowers the input cap.
  - Once the queue is full, `Snapshot` and `SendKey` must return within a deadline, and `Kill` must still end it.
- **After exit:** `Snapshot` returns the last screen; `SendKey` and `Paste` return at once; `Resize` returns `ErrExited`.
- **`StartPane` errors:** a non-empty `Output`; the existing bad-spec cases.

**Shell matrix** (`shells_test.go`): the probe from Design for every installed shell, as parallel subtests. A shell named in `GOMUX_REQUIRE_SHELLS` but missing fails. nushell's probe is verified by macOS CI.

**Everything else:**
- `go test -race -count=5 ./internal/term/`, and again under CPU load (CLAUDE.md §15).
- gofmt, `go vet` (darwin and linux), staticcheck and govulncheck are clean.
- CI is green on ubuntu and macOS, with the matrix running every required shell on both.
- **Manual:** none. Nothing is user-visible yet.

## Done when

- [x] `term.Pane`: emulator attached, replies drained through `Term.Write`, keys and paste, resize, snapshot
- [x] Snapshot types in `internal/proto`
- [x] Shell matrix passing locally (sh, dash, bash, zsh, fish); CI installs zsh and fish (ubuntu) and fish and nushell (macOS)
- [x] CLAUDE.md updated (see Design and Notes)
- [x] `go test -race ./...` and `govulncheck ./...` pass; the race tests also pass under load
- [x] `/security-review` and `/code-review` findings fixed or rejected with a reason here
- [ ] CI green; merged with Rebase and merge

## Open questions

None left. Answered at review on 2026-11-25:
1. The snapshot types live in `internal/proto` (Design, decision 1).
2. nushell is installed on macOS CI and is part of the matrix there (Design, the shell matrix).

## Notes after implementation

**Who wrote `pane.go`.** The user typed the start of it (the `Pane` type and `StartPane`) to practise Go. It went into `term.go` by mistake, so it was moved to `pane.go` as the brief places it, and the rest was completed from the reference copy.

**Deviations from the design above:**
- **`Paste` returns an error and doesn't use `x/vt`'s `Paste`** (code review, finding 1). `x/vt` writes the opening bracket, the text and the closing bracket as three pipe writes, which the reply drain reads in 4 KiB pieces. Each piece counted against the 1 MiB input cap on its own, so two things could go wrong:
  - a large paste, or any paste into a nearly full queue, lost pieces, possibly including its closing bracket, which leaves the app stuck in paste mode;
  - pieces dropped from the middle left holes in the pasted text.
  - **Now:** the pane tracks bracketed-paste mode through `x/vt`'s `EnableMode`/`DisableMode` callbacks. A full reset goes through them too (`resetModes` calls `setMode`). The pane builds the bytes itself and hands them to `Term.Write` in one call, so a paste is taken whole into an idle queue or refused whole with an error.
  - **Ordering:** an empty write through the reply pipe first acts as a barrier. The drain receives it only after passing everything before it to the `Term`, so a paste can't overtake keys.
- **Pasted text loses ESC and C1 controls** (code review, finding 4). Text containing `ESC [201~` would end the bracket early and run the rest as typed input. Copy-mode text will come from pane output, which is untrusted. Recorded in CLAUDE.md §6.6 and §13.
- **`Resize` sets the PTY first, then the emulator** (code review, finding 3). Both happen under the pane's lock, which the program's output also needs, so its redraw still lands in the resized emulator. A resize the PTY refuses (after exit) now changes nothing; before, the emulator had already been resized.
- **The shell probe has no "Device Attribute" check** (step 3 in Design). fish 4.9.3 contains no such warning text (checked with `strings`), and with DA1 unanswered it shows no prompt at all: swallowing DA1 made `TestShells/fish` fail at the prompt wait after 10 s. So the prompt wait is the real guard, and a comment says so.

**Review findings rejected, with reasons:**
- **Finding 2, "the matrix doesn't prove replies arrive"** (code review). As above: fish 4 shows no prompt until DA1 is answered, verified by experiment, so the prompt wait does prove it for fish 4. The DA1 path is also pinned directly by `TestPaneAnswersQueriesButNotHostileOnes`.
- **Finding 5, "cap pane sizes in `Pane`"** (code review). Sizes come from clients, and that trust boundary arrives with the `gomux new` item. CLAUDE.md §13 now requires the cap there.
  - Picking the limit needs the scrollback memory picture too: a scrollback line holds `cols` cells, so the column limit and `general.scrollback` bound memory together.
  - Today's callers are tests.
- **Finding 6, "default colors set by apps (OSC 10/11) are lost"** (code review). True, but sending them needs new `proto.Screen` fields and client drawing, which is the same work as the known OSC 10/11 query limit. Both are recorded together in CLAUDE.md §6.6 for when a client draws panes.
- **Finding 7, brief status and the blinking-cursor case** (code review): fixed. `TestSnapshotCursor` now covers a blinking bar (`ESC [5 q`) and a hidden steady underline.
- **`/security-review`: no findings.**
  - Every reply `x/vt` writes is fixed or numeric.
  - It has no OSC 52, no DCS/APC/PM/SOS handlers and no title report.
  - A probe confirmed that C1 controls, DEL, bidi overrides and malformed UTF-8 never reach a cell.

**Regression tests, each shown to fail on the old code** (CLAUDE.md §15):
- `TestStuckProgramDoesNotStallScreen`: with replies written straight to the PTY, it hangs in `Snapshot` (seen with `-timeout 30s`).
- `TestPaneAnswersQueriesButNotHostileOnes`: with a handler that answers title reports, the input reads `got:E]levil-title…`.
- `TestLargePasteArrivesWhole`, `TestPasteCannotEndTheBracketEarly`, `TestPasteRefusedWholeWhenQueueFull`, and the resize check in `TestPaneAfterExit` all fail with `x/vt`'s `Paste` and the old `Resize`.

**A flake caught under load and fixed before commit.** `TestStuckProgramDoesNotStallScreen` first waited for `pending > maxPending/2`. Under load the writer takes larger batches, so most queued bytes are in flight and `pending` stays small even with the queue full (3 failures in 30 loaded runs). It now waits for pending plus in-flight bytes to reach the cap, and passes 60 of 60 loaded runs.

**Verification:**
- gofmt, `go vet` and staticcheck are clean on darwin and linux; govulncheck finds nothing.
- `go test -race ./...` passes, and `term` passes `-count=5`.
- Under CPU load (12× `yes`): `term` passes 10 runs, and the paste and stuck tests pass 40.
- The shell matrix passes locally for sh, dash, bash, zsh and fish. nushell isn't installed here, so its probe is first verified by macOS CI.
