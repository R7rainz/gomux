# 002 — Prototype: a shell inside gomux

Status: In progress
Phase: prototype spike, before Phase 1 (CLAUDE.md §9)
Branch: `spike/002-single-pane`. Throwaway and **never merged**. Only the decisions it produces land on `main`.
Issue: none

## What it does

On the spike branch, `go run ./spike` takes over the terminal. It draws a status line and a bordered box slightly smaller than the window, and inside the box your shell runs. Everything inside the box should behave like a normal terminal: prompts, vim, lazygit, colors, resizing. `ctrl-q` quits and gives you your terminal back unchanged.

A flag, `-input tea|raw`, chooses how keystrokes reach the shell. The emulator libraries are compared headlessly, with the same byte sequences fed to each in a test. The interactive prototype uses `x/vt`, and a `midterm` version is built only if `x/vt` fails interactive rows.

The point is not the program. It answers three questions that everything after it is built on, and the answers get recorded in `CLAUDE.md`:
1. **Which emulator library** do we use? (§7, open decision 1)
2. **Which input path:** Bubble Tea key events, or raw bytes from stdin? (§6.7, open decision 2)
3. **Bubble Tea v1 or v2?** (§7, open decision 3)

**Out of scope:** server, socket, sessions, more than one pane, config, scrollback viewing, copy mode, tests that get merged. None of this code is reused; item 005 onwards are written fresh, using what we learn here.

## Concepts

- **TTY and PTY.** A terminal program (Ghostty) talks to a shell through a *terminal device*. A **PTY** (pseudo-terminal) is a software pair of devices:
  - the **slave** side looks like a real terminal to the shell;
  - the **master** side is held by whoever plays the terminal, which here is us.
  - What the shell prints, we read from the master. What we write to the master, the shell receives as typed input.
  - This is the core trick of every multiplexer.
  - Read more: `man 4 pty` (macOS), `man 7 pty` (Linux), "The TTY demystified" by Linus Åkesson.
- **Sessions and the controlling terminal.** When we start the shell on a PTY, it becomes a **session leader** (`setsid`) with the PTY slave as its **controlling terminal**. That's why pressing ctrl-c inside the box interrupts the shell's foreground job and not gomux: the kernel sends SIGINT to the PTY's foreground process group. Read more: `man 2 setsid`, `man 4 tty`.
- **Raw vs cooked mode (termios).** Normally the terminal driver buffers a line and handles ctrl-c itself ("cooked"). A multiplexer puts *its own* terminal into **raw mode**, so every keystroke, ctrl-c included, reaches us immediately as bytes, and must put it back on exit. Bubble Tea does this for us. Read more: `man 3 termios`, `man stty`.
- **Escape sequences.** Program output is not plain text. Interleaved with the characters are control sequences:
  - **CSI** (`ESC [ …`) moves the cursor, sets colors (**SGR**), and switches modes;
  - **OSC** (`ESC ] …`) sets the title or the clipboard.
  - Read more: "XTerm Control Sequences" (invisible-island.net/xterm/ctlseqs), vt100.net.
- **Terminal emulator / screen model.** A state machine that consumes those bytes and maintains a grid of cells (a character plus its style), the cursor position and modes. There are two grids: the **main screen** and the **alternate screen**. vim and lazygit draw on the alternate one and switch back on exit, which is why your shell output reappears. **Scroll regions** let apps scroll part of the screen. We will not write one; we pick a library.
- **Queries and replies.** Apps ask the terminal questions:
  - "what are you?" (DA, `ESC [ c`);
  - "where is the cursor?" (`ESC [ 6n`);
  - "what's the background color?" (`OSC 11`).
  - The emulator must answer by writing back into the PTY. If nobody answers, apps wait (fish shows a delay warning) or guess wrong.
- **Window size and SIGWINCH.** We tell the kernel the PTY's size with an ioctl (`pty.Setsize`, which is TIOCSWINSZ). The kernel then sends **SIGWINCH** to the app in the foreground, and the app asks for the new size and redraws. Here the box is the window size minus the border and status line.
- **Key encoding depends on the app's modes.**
  - Keys are bytes: ctrl-a is `0x01`, alt-x is `ESC x`.
  - The up-arrow is `ESC [ A` normally but `ESC O A` when the app turned on *application cursor mode* (vim and less do).
  - Pasted text is wrapped in `ESC [200~ … ESC [201~` only if the app enabled *bracketed paste*.
  - The app switches these modes by sending sequences to *our emulator*, not to Ghostty. So neither input path is free:
    - a re-encoding path must follow the inner app's modes;
    - a raw path must translate what Ghostty sends.
  - This is the heart of question 2.
- **Character width.** Emoji and CJK characters take two cells. If the app, the emulator and Ghostty disagree on a character's width, rows shift and the box border breaks. Read more: Unicode UAX #11, `wcwidth`.
- **Render throttling.** Output can arrive in floods (`cat` of a big file). We redraw from the emulator's current state at most about 60 times a second, never once per byte.

## Flows

### Startup

1. `main` parses flags. Bubble Tea v2 starts: Ghostty goes into raw mode and the alternate screen, and Bubble Tea sends a window-size message.
2. Compute the box's inner size: width − 2 (border), height − 3 (border + status line).
3. `pty.StartWithSize($SHELL, size)` starts the shell as a login shell and session leader on a new PTY; we keep the master.
   - The environment is sanitized: `TERM=xterm-256color`, `COLORTERM=truecolor`, `TERM_PROGRAM=gomux`, and `TERM_PROGRAM_VERSION` and `GHOSTTY_*` dropped.
   - Without this, shells inside the box detect Ghostty and turn on features our emulator doesn't implement.
4. Create the chosen emulator at the same size.
5. Start the goroutines: the PTY reader, the emulator-reply writer, and the input reader (raw path only).

### Output: shell → screen

1. **PTY reader goroutine:** `read(master)` gets bytes, which go to `emulator.Write`, which marks the screen dirty with a non-blocking channel send. It never blocks on the UI.
2. **Reply writer goroutine:** whatever the emulator wants to answer (DA, cursor position, colors) is written back to the master.
3. **Bubble Tea:** a 16 ms tick checks the dirty flag. `View()` draws the emulator's cells inside a Lipgloss border, plus a status line showing the emulator, input path and size.

### Input path A (`-input tea`): Bubble Tea events → bytes

1. Bubble Tea parses Ghostty's input into key, paste and mouse events.
2. `ctrl-q` quits. Anything else is encoded into bytes according to the inner app's current modes and written to the master. If the emulator offers this encoding (`x/vt` appears to; confirm in the spike), we use it; otherwise a small encoding table.

### Input path B (`-input raw`): stdin bytes → PTY

1. Bubble Tea's own input reading is turned off. Our goroutine reads raw bytes from stdin.
2. It scans for `0x11` (ctrl-q) and forwards everything else to the master. It then records what breaks, e.g. arrow keys in vim (the cursor-mode mismatch), paste, and mouse coordinates offset by the box position.

### Resize

1. Ghostty resizes, and Bubble Tea gets a size message.
2. Recompute the box, then `pty.Setsize` and `emulator.Resize`.
3. The kernel sends SIGWINCH to the shell's foreground job. The app redraws, and the output flows through the normal path.

### Error paths

- **The shell fails to start** (bad `$SHELL`) → print the error after Bubble Tea restores the terminal, exit 1.
- **The emulator panics on odd input** → the reader goroutine recovers and tells Bubble Tea to quit with the error, and the terminal is restored. The panic is recorded in the scorecard; that is a strike against the library.
- **A panic in our own goroutines** → the same recover-and-quit path. Bubble Tea only catches panics in its own `Update`/`View`.
- **The PTY read fails** (EIO when the shell exits) → treated as a normal exit.

### Shutdown and cleanup

- **The shell exits** (`exit`, ctrl-d) → the reader sees EOF/EIO → `cmd.Wait()` reaps the process → Bubble Tea quits and restores Ghostty.
- **`ctrl-q`** → close the master. The kernel sends SIGHUP to the shell's session. Wait up to 2 s, then SIGKILL the process group, reap, quit and restore.
- **In every case** Ghostty is left in cooked mode, on the main screen, with the cursor visible.

## What is needed

- **Branch/files:** `spike/002-single-pane`, kept local and deleted once the decisions are on `main`.
  - `spike/main.go` uses `x/vt` directly, with no emulator interface.
  - `spike/spike_test.go` holds the headless fixtures for both emulators, the key-encoding checks and the PTY checks.
- **Dependencies** (in the spike branch's `go.mod` only, never on `main`):
  - `github.com/creack/pty` v1.1.24. Its v2 tag is from 2023 and never became a release; v1 is the maintained line.
  - `charm.land/bubbletea/v2` v2.1.1 (stable since 2026; Go 1.26+, so fine with our 1.27).
  - `charm.land/lipgloss/v2` v2.1.1.
  - `github.com/charmbracelet/x/vt` v0.1.0. MIT. First tagged release October 2026. It shares its cell model (`ultraviolet`, `x/ansi`) with Bubble Tea v2.
  - `github.com/vito/midterm` v0.2.5. MIT. Maintained, used by Dagger. It pulls in an old pre-v1 Bubble Tea and testify.
  - `github.com/hinshun/vt10x` is only tried if both fail. It has been unmaintained since 2022.
- **On your machine:**
  - Ghostty, zsh (your `$SHELL` here), fish, bash, vim, nvim, lazygit, less and btop (1.4.8) are installed.
  - btop replaces htop in the checklist: truecolor, braille graphs, mouse and constant redraws.
- **Protocol messages, config keys:** none.

## Design

- **One process, no server, on purpose.** The questions are about terminals, not IPC. A server would only add noise.
- **Order of evaluation:**
  1. Headless fixtures for `x/vt` and `midterm`.
  2. The hands-on matrix on `x/vt` with input path A.
  3. Path B on `x/vt`.
  - `midterm` gets an interactive run only if `x/vt` fails rows. Reading its source showed it has no key encoder and isn't safe across goroutines, so an interactive run would need glue code that `x/vt` provides itself.
- **How the emulator is chosen:**
  - matrix results;
  - query replies;
  - security rows (below);
  - an API fit for the server later: reading cells and the cursor for snapshots, knowing which lines changed, reaching scrollback;
  - maintenance;
  - dependency weight.
- **Bubble Tea v2 is the presumed answer to question 3.** v1 is in maintenance (last release v1.3.10, September 2025), while v2 is stable with the cell-based renderer and the richer key events CLAUDE.md §7 wanted. The spike confirms it rather than testing v1.
- **Who does what.** You'll be asked for about 30–60 minutes of hands-on testing.
  - I write the spike and run the checks that don't need a human: feeding recorded escape sequences into each emulator and asserting the screen, and driving the PTY with scripts.
  - The interactive rows (vim feel, keys, resize, paste) need a person at the keyboard, so you run them in Ghostty with a checklist I give you, and tell me the results.
  - I fill in the scorecard and the decisions.

## Security

**Trust boundary: pane output is untrusted** (CLAUDE.md §13). The spike checks how each emulator handles hostile output, and a bad answer counts against it:
- `printf '\e]0;rm -rf ~\a'` followed by `printf '\e[21t'` (a title report request). Nothing may appear at the shell prompt.
- `printf '\e]52;c;?\a'` (a clipboard read request). Must not answer with clipboard contents.
- A title containing control characters. The status line must not interpret them.

**Other points:**
- **Dependencies:** licenses (both MIT) and maintenance are part of the decision. Spike dependencies never reach `main`'s `go.mod`; the eventual real dependency is added in item 005/006 with its §7 row.
- **Terminal restore:** Ghostty is restored on every exit path, including panics.
- **No pane I/O is logged.** The spike logs only events and sizes, if anything.
- **Runtime boundaries not touched:** socket, workspaces, plugins. None exist yet.

## Test plan

**Headless (me):** feed each emulator fixed byte sequences and assert cells and replies:
- truecolor SGR;
- alternate screen enter and exit;
- scroll region;
- wide characters (`日本語`, `👍🏽`);
- DA / cursor-position replies;
- the security rows.

Also start `sh -c 'stty size'` through the PTY at a known size and check the output.

**Interactive (you, in Ghostty).** Each row is pass, fail or partial with a note, per candidate:
1. **Shells** (zsh, then `fish`, then `bash` typed inside): prompt renders; typing, ↑ history and tab completion work; ctrl-c stops `sleep 10`; ctrl-d exits the inner shell.
2. **vim and nvim:** open a file; insert; arrows in both modes; colors; undercurl (nvim diagnostic or `:hi Error gui=undercurl`); on quit the shell screen comes back.
3. **lazygit and btop:** render, navigate, keys work; btop's graphs update smoothly; mouse click (stretch goal).
4. **less:** arrows, space, `/search`.
5. **Floods:** `seq 1 2000000` and `yes | head -n 500000`. The UI stays responsive and ctrl-c works.
6. **Unicode:** `echo "日本語 👍🏽 é"`. Aligned, border intact.
7. **Resize** the Ghostty window: the shell rewraps and vim redraws.
8. **Keys:** alt-b/alt-f in zsh; Home/End; PgUp/PgDn; F-keys in lazygit; shift-tab; paste a multi-line block into zsh. Bracketed paste must stop it running line by line.
9. **Truecolor:** `printf '\e[38;2;255;100;0mTRUECOLOR\e[0m\n'`.
10. **Startup:** fish shows no "terminal did not respond" warning.

## Done when

- [ ] Scorecard filled in below for every candidate tried, plus input paths A and B on the winning emulator
- [ ] Decisions recorded in CLAUDE.md: emulator and Bubble Tea version in §7, input path in §6.7, open decisions 1–3 removed from §11
- [ ] Spike branch left unmerged
- [ ] Brief and CLAUDE.md changes committed to `main`
- [ ] Security rows checked for the chosen emulator (CLAUDE.md §13)

## Open questions

None left. Answered at approval: use btop instead of htop; keep the spike branch local and delete it after the decisions are recorded.

## Scorecard

<!-- Filled in during the spike. -->

## Notes after implementation

<!-- Deviations from this brief, and why. -->
