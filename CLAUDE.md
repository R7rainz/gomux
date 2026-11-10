# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

gomux is in the planning stage. This file is the project plan (scope, architecture, roadmap, packaging) and the source of truth for decisions. When a decision changes, update this file in the same change. Code and this file must never disagree silently. Per-feature detail lives in briefs under `docs/briefs/` (§12).

## 1. What we are building

gomux is a local-first terminal multiplexer and session manager for developers, written in Go. A long-lived **server** owns sessions → windows → panes; each pane is a PTY running a shell or a command. Thin **clients** attach and detach over a Unix socket. Closing the terminal never kills your work.

**Positioning.** gomux does not try to replace tmux. tmux is on every server, is the default over SSH, and has decades of configs and muscle memory behind it. gomux is for the developer's own machine:

- **Projects, not just sessions.** `gomux open .` brings up a project's panes and commands from a checked-in `.gomux.toml`, and reattaches if the project is already running.
- **Good with zero config.** Mouse, truecolor, clipboard, a short escape delay and visible keybinding hints all work out of the box.
- **One searchable command palette** for every action, including plugin actions.
- **Small, optional plugins for developers:** git, system monitor, docker, project tasks.

It works over SSH if installed on the remote host, but SSH use does not drive design decisions.

**The design must serve these workflows:**

1. `cd ~/code/api && gomux open .` → session `api` with the panes declared in `.gomux.toml`. Running it again attaches to the same session.
2. Close the terminal → processes keep running. `gomux attach api` brings them back with their screens intact.
3. Move between projects from the palette ("Switch session", "Open recent project").
4. Split, zoom, resize and navigate with the prefix key or the mouse.
5. Glance at the status bar: branch and dirty state, running containers, CPU/memory, windows with new activity or a finished command.

## 2. Current state

Scaffold only: `go.mod` (module `github.com/R7rainz/gomux`, Go 1.27.3) and a placeholder `cmd/main.go`. `internal/` is empty. There are no dependencies, tests, CI or license yet. The next step is Phase 0 (see §9).

## 3. Commands

These are the planned commands. Phase 0 moves `main` to `cmd/gomux/`; until then use `./cmd`.

```sh
go run ./cmd/gomux                              # client; autostarts the server
go build -o bin/gomux ./cmd/gomux
go test ./...
go test -race ./...                             # required before every change lands; the server is concurrent
go test ./internal/layout -run '^TestSplit$' -v # single test
go test ./internal/proto -fuzz FuzzDecode -fuzztime 30s
go vet ./... && staticcheck ./...
govulncheck ./...                               # known-vulnerability scan of code + deps (§13)
grep -H '^Status:' docs/briefs/*.md             # status of every work item (§12)
goreleaser release --snapshot --clean           # local packaging dry run, once .goreleaser.yaml exists
```

Keep a dev build away from an installed gomux: `GOMUX_SOCKET=/tmp/gomux-dev.sock bin/gomux ...`. Debug the server with `bin/gomux server --foreground --log-level debug` (logs go to stderr). Both are Phase 1 deliverables because a daemon is undebuggable without them.

## 4. Working rules

- Work incrementally, one work item at a time, always in this order (§12): titled brief → user approval → implementation → tests (`go test -race ./...`) → security check (§13) → update this file if scope or architecture moved. No code before the user approves the brief.
- Never generate the whole app in one pass. No placeholder implementations, stubs for unsupported features, or "for later" interfaces and options.
- Don't silently change a decision recorded here. Propose it, then edit the relevant section.
- Use concrete types first. Add an interface only at a real seam (two or more implementations today, or a test that needs it). No DI frameworks or extra layers.
- Prefer the stdlib. Every new dependency must solve a meaningful problem and gets a row in §7 saying why.
- Behavior is config-driven. User-visible defaults live in `internal/config` from Phase 1 on, never as scattered constants.
- The core must be fully usable with zero plugins. A plugin may never block, crash or slow the core.
- Never log pane input/output, environment values or palette input.
- **Out of scope** unless this file changes: databases, HTTP/gRPC for local IPC, microservices, message brokers, Redis, Kubernetes, cloud accounts/auth, telemetry, AI features, a plugin marketplace or registry, and web or other complex frontends.

## 5. Scope

### CLI surface

```
gomux                     attach to general.default_session, creating it if missing
gomux new [name] [-c dir] create a session and attach
gomux ls                  list sessions (name, windows, attached clients, created)
gomux attach <name>
gomux detach              inside a pane: detach the clients of that pane's session (prefix+d is the usual way)
gomux kill <name>         kill a session
gomux rename <old> <new>
gomux open [dir]          create or attach the project session for dir (default .)    [Phase 6]
gomux reload              reload config                                               [Phase 6]
gomux config check        validate config without starting anything
gomux config default      print the default config
gomux kill-server         stop the server and every session
gomux server              hidden; runs the server (--foreground for dev)
gomux --version           binary version + protocol version
```

`attach` and `new` refuse to run inside a gomux pane (`$GOMUX` is set) unless `--force` is given, so nobody nests sessions by accident.

### Feature tiers

**v0.1: usable as a daily driver**
- Server autostart, Unix socket, versioned handshake, graceful shutdown with documented process semantics.
- Sessions: new, ls, attach, detach, kill, rename. They survive client disconnects.
- Windows: create, close, switch, rename. Panes: split horizontally and vertically, close, directional navigation, resize, zoom toggle.
- VT emulation through a library: 256 colors and truecolor, alternate screen, cursor, wide characters. bash, zsh, fish, vim, nvim, htop and lazygit work.
- Per-pane scrollback with keyboard and mouse scrolling.
- Mouse: click to focus, wheel to scroll. Mouse events are forwarded to apps that enable mouse mode.
- Status bar with the session name and window list.
- Prefix-based default keybindings; TOML config loading with validation.
- The user's terminal is always restored when the client exits or crashes.

**v0.x: the developer features (the reason to pick gomux)**
- Project workspaces: `.gomux.toml`, `gomux open`, a trust prompt, and a recent-projects list.
- Built-in layouts: `even-horizontal`, `even-vertical`, `main-vertical` (main + secondary), `tiled`.
- Command palette backed by a central command registry; a keybinding hint overlay (which-key style).
- Copy mode: select and search scrollback, copy to the system clipboard via OSC 52. OSC 52 from apps (an nvim yank) is passed through.
- Multiple clients on one session.
- Config reload, keybinding remapping, themes for gomux's own chrome.
- Built-in plugins: git, system monitor, docker, workspace tasks.
- Window indicators for activity, bell and "command finished".
- Shell completions for bash, zsh and fish, plus a man page.

**Later, only when there is a concrete need**
- External WASM plugins (Phase 8).
- `gomux save`: write the current layout back to `.gomux.toml`. Recreate the last layout after a server restart (layout, cwd and commands only).
- Floating/scratch panes, popups.
- Scripting commands for editor integrations (`gomux split -- cmd`, `gomux send-keys`).
- Synchronized input across panes.
- OSC 8 hyperlinks, kitty keyboard protocol, undercurl/colored underlines (nvim diagnostics). Check what the emulator library already supports first; some of this may come for free.

**Non-goals**
- Native Windows (no ConPTY). WSL counts as Linux.
- tmux config or keybinding compatibility; tmux control mode (iTerm2 `-CC`).
- Keeping processes alive across a server exit. Only layout and commands can be restored.
- Images in panes (sixel, kitty graphics).
- Networked or shared sessions.
- A scripting language or custom layout DSL.

## 6. Architecture

### 6.1 Processes and socket

- One binary. `gomux server` is the server; every other subcommand is a client.
- **Autostart:** if the socket is not accepting connections, the client removes a stale socket (connect fails with ECONNREFUSED), re-execs itself as `gomux server` with `SysProcAttr{Setsid: true}` and stdio on `/dev/null`, then waits for the socket. Go cannot fork, so re-exec is the way to daemonize.
- **Socket path:** `$GOMUX_SOCKET`, else `$XDG_RUNTIME_DIR/gomux/default.sock`, else `$TMPDIR/gomux-$UID/default.sock` (macOS has no `XDG_RUNTIME_DIR`; `$TMPDIR` falls back to `/tmp`). The directory is 0700 and the socket 0600. The server checks the peer UID (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED`/getpeereid on macOS) and rejects other users.
- The server is the source of truth for runtime state, outlives its clients, and exits when the last session ends.
- **Upgrades:** `brew upgrade` or `apt upgrade` does not restart a running server. The client sends its protocol version in the handshake. On a mismatch it prints which versions are involved and the two ways out: `gomux kill-server` (ends sessions) or keep using the old binary until done. It never talks across versions silently.

### 6.2 Packages

Create a package only when its phase needs it.

```
cmd/gomux/        main: subcommand dispatch with stdlib flag; no logic
internal/proto/   envelope, message types, length-prefixed framing, protocol version
internal/server/  listener, client connections, event loop, Session/Window/Pane model, command registry
internal/layout/  layout tree: split, close, resize, navigate, compute rects. Pure, no I/O
internal/term/    PTY, process spawn/lifecycle, VT emulator wrapper (one per pane)
internal/client/  Bubble Tea program: input, keybindings, composing panes and chrome, palette UI
internal/config/  defaults, XDG paths, TOML loading, validation, keybinding parsing
internal/plugin/  built-in plugin interface and manager; one subpackage per plugin
```

Dependencies point one way: `client → proto, config`; `server → proto, layout, term, config, plugin`. `client` never imports `server` or `term`, and `layout` imports nothing internal. A cycle means the design is wrong. Don't paper over it with an interfaces package.

### 6.3 Concurrency

- **One event-loop goroutine** in the server owns every session, window, pane and layout. Client connections and panes talk to it over channels, and it never blocks on I/O. This replaces lock-per-struct designs.
- **Each pane** has a reader goroutine that continuously drains the PTY master into the pane's emulator (guarded by its own mutex), marks the pane dirty and notifies the loop. It must never stall: if nobody reads the PTY, the program in the pane blocks on write. A second goroutine waits on `cmd.Wait()`, reaps the process and reports its exit status.
- **Each client** has a reader goroutine (decode frames → loop) and a writer goroutine with a bounded queue. If a client is slow, its queued screen updates are dropped and replaced by a fresh snapshot. A slow client never blocks the loop or a pane.
- **Render tick:** dirty panes go out to clients at most about 60 times per second, so `cat bigfile` cannot flood the socket.
- Every goroutine has an owner and a `context` for shutdown. `go test -race` must pass.

### 6.4 Data model

```
Server   Sessions, Clients, PluginManager
Session  ID, Name, Windows (ordered), ActiveWindow, CreatedAt, AttachedClients,
         Workspace (root dir, source .gomux.toml, if any)
Window   ID, Name, Layout tree, ActivePane, Zoomed
Pane     ID, Term (PTY + process + emulator), Cwd, Size, Title, ExitStatus
```

IDs are monotonically increasing integers per server. They are never reused and never slice indexes. Names are user labels, and session names are unique per server. A window's displayed number is its position, which is separate from its ID.

### 6.5 Layout tree (`internal/layout`)

- A node is either `Split{Dir, Children, Ratios}` or `Leaf{PaneID}`. Splits are n-ary: splitting a pane in the same direction as its parent adds a sibling instead of nesting.
- `Compute(tree, w, h) → map[PaneID]Rect` is pure and deterministic. Borders take one cell, rounding remainders go to the last child, and a minimum pane size is enforced. A split that would break the minimum is refused, not squeezed.
- Resizing shifts the ratio between a pane and its neighbor in the nearest ancestor split of that direction.
- Navigation picks the adjacent pane in the requested direction with the largest shared edge. Ties go to the most recently active pane.
- Named layouts are functions that rebuild the tree from the window's pane list. Zoom renders only the active pane and leaves the tree untouched.
- This package gets the most tests: table-driven cases plus property checks (rects tile the window exactly, nothing overlaps, every pane has a rect).

### 6.6 Terminal engine (`internal/term`)

- **Spawn** with `pty.StartWithSize` (creack/pty). The shell is `general.shell`, else `$SHELL`, else `/bin/sh`, started as a login shell (argv0 `-zsh`, like tmux) so the profile's PATH loads. This matters on macOS.
- **Environment:** a new session inherits the environment of the client that created it, sent in the request. The server's own environment may be stale (PATH, virtualenvs). Panes also get `TERM=xterm-256color`, `COLORTERM=truecolor`, `GOMUX=<socket path>` and `GOMUX_PANE=<pane id>`. `TERM` must advertise only what the emulator actually implements. Shipping our own terminfo entry is a non-goal.
- **Output:** PTY bytes go into the emulator (screen, alternate screen, cursor, modes, scrollback ring). Output is never treated as plain text.
- **Query replies:** the emulator's answers to device attributes, cursor-position reports and OSC 10/11 color queries are written back to the PTY. Without them, apps such as nvim and fish stall or misbehave.
- **Resize:** client SIGWINCH → `resize` message → server recomputes the layout → `pty.Setsize` per pane (the kernel delivers SIGWINCH to the pane's foreground process group) → emulator resize. With several clients on one session, the most recently active client's size wins.
- **Pane exit:** when the process exits, the pane closes, along with its window or session if it was the last one.
- **Killing a pane:** close the PTY master (SIGHUP to the session), wait a short grace period, then SIGKILL the process group.
- **Server shutdown** (`kill-server`, SIGTERM, SIGINT): stop accepting connections, hang up every pane, wait out the grace period, SIGKILL leftovers, remove the socket and exit. Processes do not survive server shutdown; this must be stated in the README. If the server crashes, the kernel closes the PTY masters and the result is the same. The server ignores SIGHUP.
- **Restoring the client terminal:** on exit, on panic and on server EOF, the client leaves the alternate screen, shows the cursor, turns off mouse, bracketed-paste and focus reporting, and restores cooked mode. Bubble Tea handles normal exit; the panic and EOF paths need explicit handling.

### 6.7 Rendering and input

- **The server runs the emulators; clients never parse pane output.** A reattaching client needs the current screens, and several clients must see the same thing.
- **Server → client:** on attach, the full window state (pane rects, metadata) plus a full screen for each visible pane. After that, per-pane dirty lines (cells as style runs) and the cursor.
- **The client composes** pane contents at their rects, borders, the status bar, the palette and overlays, using Bubble Tea and Lipgloss.
- **Input:** prefix and bound keys are handled in the client and become requests. Everything else is sent as bytes to the active pane. Pressing the prefix twice sends it through literally. Mouse events over a pane go to the app (translated to pane coordinates) if it enabled mouse mode; otherwise gomux uses them for focus and scrolling. Keep the ESC/alt disambiguation delay short (≤25 ms by default); tmux's 500 ms default is a well-known vim annoyance.
- **Risk, to spike in Phase 2:** turning Bubble Tea key events back into the exact bytes apps expect (alt/ctrl combinations, function keys, bracketed paste) may lose information. If it does, the client reads raw stdin, detects the prefix itself, and uses Bubble Tea only for rendering and overlays.

### 6.8 IPC protocol (`internal/proto`)

- **Frame:** a 4-byte big-endian length followed by a JSON body, with a maximum frame size (e.g. 16 MiB). The decoder is a trust boundary, so it is fuzzed.
- **Envelope:** `{"version":1,"type":"pane.create","request_id":"123","payload":{}}`. Request types are `noun.verb`. Responses echo `request_id` and carry either a payload or `error{code, message}`. Server events carry a type and no `request_id`.
- **Handshake:** the first client frame is `hello` (protocol version, client version, terminal size, TERM, environment). The server answers `welcome`, or `error{code:"version_mismatch"}` and closes the connection.
- Byte payloads (keyboard input) are `[]byte`, which `encoding/json` encodes as base64.
- `shortcut: screen updates are JSON too; if profiling shows encoding or bandwidth cost, add a binary frame type behind the same length prefix.`

### 6.9 Configuration (`internal/config`)

**Paths.** Resolve XDG variables manually. Do not use `os.UserConfigDir`: on macOS it returns `~/Library/Application Support`, and developers expect `~/.config`.

| What | Path |
|---|---|
| config | `$XDG_CONFIG_HOME/gomux/config.toml` → `~/.config/gomux/config.toml` |
| state (logs, recent projects, trusted workspaces) | `$XDG_STATE_HOME/gomux` → `~/.local/state/gomux` |
| data (external plugins) | `$XDG_DATA_HOME/gomux` → `~/.local/share/gomux` |

**Loading.**
- **Precedence:** built-in defaults < user config < project `.gomux.toml` (only its workspace, panes, tasks and plugins sections) < CLI flags.
- **Config is a Go struct with defaults filled in code**, and TOML decodes on top of it. `gomux config default` prints that struct, which makes it the documentation.
- **Unknown keys** produce a warning naming the key path (BurntSushi/toml `MetaData.Undecoded`), shown in the status bar and never fatal, so an older binary can still read a newer config.
- **Invalid values** (wrong type, unknown key name, missing shell) are errors with file:line. At startup the command fails with that error. On reload the previous config stays active and the error is shown.
- **Reload** applies keybindings, appearance and plugin settings. Running panes are not touched; a shell change applies only to new panes.

**Keybindings.**
- Keybindings map **command IDs** (the same IDs the palette uses) to keys. Key syntax: `ctrl-space`, `alt-h`, `shift-tab`, `f5`, a single character.
- The default prefix is `ctrl-space`. On macOS that key switches input sources when more than one is enabled, so the docs must show how to change it.

The spec's sample config, trimmed of options a multiplexer cannot honor. `font_size` is gone because the host terminal owns fonts. `theme` styles gomux's own chrome, not app colors.

```toml
[general]
shell = ""               # empty → $SHELL
default_session = "main"
scrollback = 10000
mouse = true

[appearance]
theme = "dark"
status_bar = true
pane_borders = true
rounded_corners = true

[keybindings]
prefix = "ctrl-space"
split_right = "|"
split_down = "-"
close_pane = "x"
next_window = "l"
previous_window = "h"
command_palette = "p"

[plugins]
enabled = ["git", "sysmon"]

[plugins.git]
refresh_interval = 10
```

### 6.10 Project workspaces

- **`.gomux.toml`** lives in the project root. `gomux open <dir>` reads exactly `<dir>/.gomux.toml`; there is no upward search.
- **Session name** is `name` from the file, else the directory's basename. If a session with that root already exists, `open` attaches to it. Otherwise it creates the session from the file, resolving paths relative to the file. With no file, it creates a plain session in that directory.
- **Trust:** a cloned repo must never run commands on its own. The first `open` of a file, and any `open` after its contents change, lists the commands it will run and asks y/N. Approvals are stored as path + SHA-256 in `~/.local/state/gomux/trusted.toml` (the same idea as `direnv allow`).
- **Pane commands are typed into an interactive shell** rather than run as the pane's process. When `go test ./...` finishes, the pane stays open with its output, and ↑ reruns it.
- **Schema** (from the spec): `name`; `[workspace] layout, working_directory`; `[[panes]] name, command`; `[plugins] enabled`; `[tasks]` for the tasks plugin. `layout` must name one of the built-in layouts.

### 6.11 Command registry and palette

- **One registry in the server.** Each command has an ID (`pane.split-right`), a title and a handler. Core actions and plugin commands register there. Keybindings and the palette both refer to command IDs, so plugins never couple to the client.
- **The client fetches the command list** and adds its own few local actions (scroll, copy mode, open palette). Running a command sends `command.run{id, args}`.
- **Fuzzy matching** is simple subsequence scoring, with no dependency.

### 6.12 Plugins

**Built-in plugins** are Go code compiled into the binary. Start with this interface and grow it only when a plugin needs more:

```go
type Plugin interface {
    Name() string
    Run(ctx context.Context, h Host) error // returns when ctx is canceled
}
```

- **`Host`** lets a plugin set status-bar segments, register commands, open a pane running a command, and read its config section.
- **Lifecycle:** the manager runs each enabled plugin per session, scoped to that session's workspace root. Its context is canceled on disable, session close or reload. Panics are recovered and logged, and the plugin is marked failed while the core carries on.
- **Polling** happens only at the configured `refresh_interval`, and is paused while no client is attached.
- **Prefer existing CLIs over SDKs and custom UIs.** If a CLI is missing, the plugin shows nothing and logs once.
  - **git:** `git status --porcelain=v2 --branch`, with no go-git.
  - **docker:** `docker ps --format '{{json .}}'`, with no Docker SDK. Logs open a pane running `docker logs -f <id>`, and start/stop are palette commands.
  - **sysmon:** gopsutil for CPU, memory, disk and network without cgo.
  - **tasks:** `[tasks]` in `.gomux.toml` (`test = "go test ./..."`). "Run task: test" runs in a dedicated pane that is reused on rerun, and the pane border shows the last exit status.

**External plugins** (Phase 8, wazero; build only once the built-ins have proven the `Host` API):
- **Install** by placing a directory at `~/.local/share/gomux/plugins/<name>/` containing a `plugin.toml` manifest (name, version, `api_version`, entry `.wasm`, permissions). There is no registry and no remote install.
- **Permissions:** everything is denied by default. Permissions are explicit host capabilities (`status`, `commands`, `panes.open`, `exec:<binary>`, `fs.read:workspace`, …), shown to the user when they enable a plugin. A plugin gets no WASI filesystem or network unless a permission grants it.
- **Limits:** a memory cap (wazero memory limit) and per-call timeouts through context cancellation.
- **ABI, decided by a spike:**
  - (a) a WASI command module that exchanges the same JSON messages as `internal/proto` over stdin/stdout (language-agnostic);
  - (b) exported functions over shared memory.
  - Prefer (a) unless latency rules it out.

### 6.13 Logging

- The server logs through slog's TextHandler to `~/.local/state/gomux/server.log`; with `--foreground` it logs to stderr. Set the level with `--log-level` or `GOMUX_LOG_LEVEL`.
- **Logged:** server start and stop, client connect and disconnect, pane spawn (command, pid) and exit (status), PTY errors, plugin lifecycle.
- **Never logged:** pane I/O, environment values, palette input.
- `shortcut: the log is truncated at server start when over 10 MB; add rotation if anyone needs history.`

## 7. Tech stack

| Need | Choice | Why / notes |
|---|---|---|
| PTY | `github.com/creack/pty` | De facto standard, pure Go, Linux/macOS/BSD. No Windows, which matches the non-goal. |
| VT emulation | **Spike in Phase 2.** Candidates: `charmbracelet/x/vt`, `vito/midterm`, `hinshun/vt10x`. `danielgatis/go-vte` is a parser only. | Don't write our own emulator. Test each candidate with nvim, htop, lazygit, fish and less. Check alternate screen, scroll regions, wide characters and emoji, truecolor, query replies, undercurl, and maintenance activity. `x/vt` is from the same org as Bubble Tea and Lipgloss. |
| Client UI | Bubble Tea + Lipgloss | From the spec. Bubble Tea is for the interface only, not PTYs or emulation. Prefer v2 (cell-based renderer, better key handling) if it is stable; verify. |
| Config | `github.com/BurntSushi/toml` | `MetaData.Undecoded()` provides unknown-key warnings. |
| System stats | `github.com/shirou/gopsutil` | Cross-platform without cgo. Imported only by the sysmon plugin. |
| WASM plugins | `github.com/tetratelabs/wazero` | Pure Go, no cgo. Phase 8 only. |
| Stdlib | `log/slog`, `flag`, `net` (unix), `encoding/json`, `encoding/binary`, `testing` | About a dozen subcommands don't need cobra; shell completions are written by hand. No testify. |
| Release (build-time only) | GoReleaser (bundles nFPM) | One config produces every package format (§10). |
| Lint | `go vet` + `staticcheck` | |

## 8. Requirements

- **Platforms:**
  - Tier 1, tested in CI: Linux and macOS on amd64 and arm64.
  - FreeBSD probably builds (creack/pty supports it) but is not promised.
  - Windows is a non-goal.
- **Build:**
  - Every release binary is built with `CGO_ENABLED=0`, which gives static binaries and trivial cross-compiling. Every dependency must stay cgo-free.
  - The `go` directive in `go.mod` stays at the minimum version actually needed, because AUR and Homebrew source builds use the distro's Go.
- **Runtime:**
  - **Required:** nothing.
  - **Optional:** the `git` and `docker` CLIs, for their plugins.
  - **Host terminal:** UTF-8 and 256 colors; truecolor recommended.
- **Non-functional targets** (measure, don't assume):
  - An idle server uses about 0% CPU: no timers fire when nothing changes and no client is attached.
  - Typing feels no slower than in the bare terminal.
  - `yes` or `cat` of a 100 MB file in one pane keeps the UI responsive and other panes unaffected, and memory stays bounded by the scrollback setting.
  - `kill -9` on a client never affects sessions. A dead server never leaves the user's terminal in raw mode.

## 9. Roadmap

This is the spec's phase order with three deliberate changes, marked ⚠. Each phase ends with its exit criteria met and `go test -race ./...` green.

0. **Repo hygiene.**
   - LICENSE, README.
   - Move `main` to `cmd/gomux/`, so `go install …/cmd/gomux@latest` names the binary `gomux`; with `main` in `cmd/` it would be named `cmd`.
   - CI on ubuntu and macOS: vet, staticcheck, `test -race`, `govulncheck`, plus a weekly scheduled `govulncheck` run.
   - `.goreleaser.yaml` with a snapshot build in CI, so packaging never rots.
1. **Foundation.**
   - Config defaults and paths; proto framing and handshake.
   - Server autostart, socket permissions, peer check, stale-socket handling.
   - `server --foreground`, `kill-server`, `ls`.
   - Exit: the client autostarts the server, `ls` round-trips, and a version mismatch fails cleanly.
2. **Terminal engine.**
   - Emulator spike and decision, recorded in §7. Input-path spike and decision, recorded in §6.7.
   - PTY spawn, I/O, resize, exit, kill. A single pane rendered from emulator snapshots and updates.
   - Exit: nvim, htop and fish are usable in one pane, resize works, and no zombie processes are left.
   - ⚠ VT emulation moves here from the spec's Phase 5. Splits and reattach both need server-side screen state, so they cannot be built on raw byte forwarding. Phase 5 hardens the emulator.
3. **Sessions.**
   - new, ls, attach, detach, kill, rename; multiple sessions; the detach keybinding; `GOMUX` environment variables; the nested-attach guard.
   - Exit: close the terminal, reattach, and the process is still running with its screen intact.
4. **Windows and panes.**
   - The layout package; split, close, navigate, resize, zoom; windows; status bar; mouse focus and scroll.
   - → **Release v0.1.0** (§10, the "day one" channels).
5. **Terminal reliability.**
   - Scrollback and copy mode with OSC 52; multiple clients.
   - Alternate-screen and wide-character edge cases, query replies, focus-event passthrough.
   - Terminal restoration on crash; throughput (`yes`, large `cat`).
6. **Configuration and workspaces.**
   - Full TOML schema, validation, reload, keybinding remapping, themes.
   - `.gomux.toml`, `gomux open`, trust, built-in layouts, recent projects.
7. **Built-in plugins.**
   - Plugin manager, then git, sysmon, docker and tasks.
   - ⚠ The command registry and palette move here from Phase 9, because plugins expose their actions through them.
8. **External plugins.**
   - wazero, manifests, permissions, ABI.
   - ⚠ Build only after Phase 7 has shown the `Host` API is stable and someone needs a plugin the built-ins can't cover.
9. **Polish.**
   - Hint overlay, activity and bell indicators, profiling, docs and man page.
   - The hosted apt repo and the homebrew-core submission (§10).

## 10. Packaging and distribution

Target installs: `brew install`, `yay -S` / `paru -S` (AUR helpers that wrap pacman), `pacman -U`, and `apt install`. What each channel actually takes:

**One pipeline builds everything.** Pushing a SemVer tag (`vX.Y.Z`) runs GoReleaser in GitHub Actions. It produces:
- static binaries for linux and darwin on amd64 and arm64;
- `.tar.gz` archives containing the binary, LICENSE, README, man page and completions;
- `checksums.txt`;
- `.deb`, `.rpm`, `.apk` and Arch `.pkg.tar.zst` packages via nFPM.

**Versioning.** The version is set with `-ldflags "-X main.version=…"`, falling back to `debug.ReadBuildInfo()` for `go install` builds. `gomux --version` prints the binary version and the protocol version.

**Before the first release:**
- A LICENSE. homebrew-core, the AUR and Debian all expect an OSI license; MIT, Apache-2.0 or ISC (which tmux uses).
- `main` at `cmd/gomux/` (Phase 0).
- Confirm the name `gomux` is free in homebrew-core, the AUR and the Debian/Ubuntu archives.
- A man page and bash/zsh/fish completions in the repo, shipped in every package.

**Channels:**

- **Homebrew** (macOS and Linux). *Day one.*
  - `brew install R7rainz/tap/gomux`. GoReleaser pushes the formula or cask to `R7rainz/homebrew-tap` using a token with write access to that repo.
  - GoReleaser has deprecated `brews` (formulae) in favor of `homebrew_casks`; check its current docs.
  - Prebuilt macOS binaries installed as a cask can hit Gatekeeper quarantine unless notarized, which needs a paid Apple Developer account. A formula that builds from source avoids this.
  - homebrew-core comes later and needs notability (star, fork and watcher thresholds), a stable release, a source build and a `test do` block.
- **AUR** (yay, paru). *Day one.*
  - `yay -S gomux-bin`. GoReleaser's `aurs` publishes the `gomux-bin` PKGBUILD, which needs an AUR account and an SSH key stored as a CI secret.
  - A source package `gomux` (`makedepends=(go)`) follows Arch's Go packaging guidelines: `-trimpath -buildmode=pie -mod=readonly -modcacherw`.
  - `gomux-git` is optional.
- **pacman official repos.** *Not in our control.*
  - Getting into `[extra]` needs an Arch package maintainer to adopt the package once it is popular.
  - Until then, users have the AUR or the `.pkg.tar.zst` attached to each release (`sudo pacman -U <url>`).
- **apt** (Debian and Ubuntu).
  - *Day one:* a `.deb` on each GitHub release (`sudo apt install ./gomux_<ver>_amd64.deb`).
  - *When users ask:* a signed apt repo so `apt install gomux` and upgrades work. Either a hosted service with an OSS plan (e.g. Cloudsmith; check current terms) or self-hosted aptly/reprepro on GitHub Pages, both signed with a GPG key.
  - Official Debian/Ubuntu archives need a sponsor and every Go dependency packaged separately, so they are long-term only. A Launchpad PPA builds offline from vendored source; possible but heavy, so skip it.
- **Free from the same pipeline:** `.rpm` (dnf, zypper), `.apk` (Alpine), plain tarballs, and `go install github.com/R7rainz/gomux/cmd/gomux@latest`.
- **Skip:**
  - Snap: a multiplexer needs classic confinement, which needs manual store approval.
  - Flatpak: the sandbox fights PTYs and spawning host shells.
  - `curl | sh` installers.
  - nixpkgs: the community can add it.

**Package contents:**
- `/usr/bin/gomux` and `/usr/share/man/man1/gomux.1.gz`.
- Completions in the vendor directories: `/usr/share/bash-completion/completions/gomux`, `/usr/share/zsh/site-functions/_gomux`, `/usr/share/fish/vendor_completions.d/gomux.fish`.
- The license in `/usr/share/licenses/gomux/` (Arch) or `/usr/share/doc/gomux/copyright` (Debian).
- No systemd unit and no post-install scripts. The server starts on demand per user, and a package upgrade never restarts a running server (§6.1).

**CI secrets:** `GITHUB_TOKEN`, a Homebrew tap token, the AUR SSH key, and later a GPG key for the apt repo and for signed checksums.

## 11. Open decisions

Resolve each one, then record the answer in the section it affects.

1. VT emulator library (Phase 2 spike) → §7.
2. Input path: Bubble Tea key events or raw-stdin pass-through (Phase 2 spike) → §6.7.
3. Bubble Tea v1 or v2 → §7.
4. License → §10.
5. Default prefix key: `ctrl-space` (conflicts with macOS input switching and editor completion) or `ctrl-a`/`ctrl-b` → §6.9.
6. Server exits when the last session ends (assumed yes, like tmux) → §6.1.
7. Size policy for multiple clients (assumed: latest active client wins) → §6.6.
8. External plugin ABI (Phase 8 spike) → §6.12.

## 12. Work items and briefs

Every feature, fix or chore is a **work item** with a number and a title. Each one gets a written brief that the user approves **before any code is written**.

**Naming.**
- Title: `NNN — Title`, with a zero-padded sequence number, e.g. `007 — Layout tree`.
- Brief: `docs/briefs/NNN-kebab-title.md`, copied from `docs/briefs/TEMPLATE.md`.
- Branch: `<type>/NNN-kebab-title`, e.g. `feat/007-layout-tree`.
- One work item = one branch = one PR. A roadmap phase splits into several work items, each small enough to review in one sitting.

**Status** is the `Status:` line at the top of each brief: `Draft → Approved → In progress → Done` (or `Dropped`). List all of them with `grep -H '^Status:' docs/briefs/*.md`.

**Every work item goes through these steps:**
1. **Brief.** Read the code the item touches, then fill in every section of the template and set `Status: Draft`. The brief describes this codebase, not a generic design.
2. **Review.** Walk the user through it: what the feature does, each flow, the concepts involved, what is needed, the risks. Answer questions and revise.
3. **Approval.** Only the user approves. Set `Status: Approved` only after they say so explicitly. No code before that.
4. **Implement** to the brief, committing as in §14. If the design has to change, stop, update the brief, and get the change approved.
5. **Verify:** the brief's test plan, `go test -race ./...`, and the security checks in §13.
6. **Close:** set `Status: Done`, record any deviation in the brief, update this file if scope or architecture moved, then prepare the PR (§14).

**What a brief must make clear** (the template has a section for each):
- What the feature does from the user's side, and what it deliberately doesn't.
- The concepts needed to understand it (e.g. PTY master/slave, process groups, SIGWINCH), each in a few plain sentences with a pointer to read more.
- Every flow, step by step: the happy path, error paths, shutdown and cleanup, and which process and goroutine does each step.
- What is needed: packages and files touched, new types, protocol messages, config keys, dependencies (with why) and external tools.
- Design decisions and the alternatives that were rejected.
- Security: the trust boundaries it touches and how each is handled (§13).
- The test plan and "done when" criteria.

**Small changes** (a typo, a dependency bump, a one-line fix with no new behavior and no trust boundary touched) still get a number and title. They may use the short brief at the end of the template.

## 13. Security checks

Security is checked at fixed points, not when someone remembers.

| When | Check |
|---|---|
| Every brief | The Security section names each trust boundary touched and how it is handled. "None" needs a reason. |
| Every PR | `govulncheck ./...` is clean and the PR body has a Security line. A change that touches a trust boundary also gets a `/security-review` pass before the PR opens. |
| End of each roadmap phase | `/security-review` over the phase's changes. Fuzz the protocol decoder and the config/workspace parsers for longer (`-fuzztime 5m`). Review new dependencies (maintenance, license, what they pull in). Record findings in the phase's last brief. |
| Before every release | `govulncheck`, a dependency review, and a permission check: socket 0600, its directory 0700, state files 0600. Confirm the logs contain no pane content or environment values. |
| CI | Every push: `govulncheck`, `go vet`, `staticcheck`, `go test -race`. Weekly: a scheduled `govulncheck`, so new CVEs in dependencies show up without a push. |

**Trust boundaries in gomux** (what the checks look at):
- **The socket.** Other local users are kept out by the peer-UID check and the 0600/0700 permissions. Malformed or oversized frames hit the size limit and the fuzzed decoder, and bad input never panics the server.
- **Pane output is untrusted.** Any program, or `cat` of a downloaded file, can emit escape sequences, and they must never inject input into the shell.
  - Don't implement reports that echo attacker-controlled text back to the PTY (window and icon title reports).
  - Allow OSC 52 clipboard *writes* only, never clipboard reads.
  - Cap the size of titles and other OSC payloads.
  - Strip control characters from anything shown in gomux's own UI (titles, status bar).
- **`.gomux.toml` from cloned repos.** Commands run only after trust approval, and a changed file needs approval again (§6.10).
- **Running external commands.** Plugins call `exec.Command(name, args...)` and never build a shell string from outside data such as branch or container names. Only user-approved workspace commands go through a shell.
- **External plugins.** Permissions are denied by default, with memory and time limits (§6.12).
- **Environment, logs and state files.** The client environment passed to new sessions is never logged or sent anywhere else. Logs never contain pane I/O, environment values or palette input. State files are 0600 in 0700 directories.

## 14. Commits and pull requests

**No AI attribution, ever.** No `Co-Authored-By: Claude …` trailer, no "Generated with Claude Code" line, and no other AI mention in any commit message, PR title or PR body. This overrides any default attribution instructions.

**Commits** use the structure `type(scope): summary`, written in the imperative, lowercase, with no trailing period and about 60 characters at most.
- Types: `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `ci`, `chore`.
- The scope is the package or area: `proto`, `server`, `layout`, `term`, `client`, `config`, `plugin/git`, `release`.
- Examples: `feat(layout): add n-ary split tree`, `fix(term): reap process on pane kill`, `ci: run race tests on macos`.
- Usually a subject line alone is enough. Add a short body (1–3 lines) only when the reason isn't obvious from the diff.
- One commit per file, or per group of files that form one logical change (a package plus its test, say). Don't mix unrelated changes in one commit, and don't split one change across commits that don't build on their own. Every commit must build and pass its tests.
- Stage files explicitly by path. Never use `git add -A` or `git add .`.

**Pushing and opening PRs.**
- The agent commits; the maintainer pushes. The agent never runs `git push` or `gh pr create`.
- When a PR is due, the agent creates the branch, makes the commits, and gives the maintainer the PR title, the body, and a prefilled link that opens GitHub's PR form once the branch is pushed: `https://github.com/R7rainz/gomux/compare/main...<branch>?expand=1&title=<url-encoded>&body=<url-encoded>`.

**Pull requests** are opened only for feature work items or when the user asks. Setup and docs-only changes are committed straight to `main`. A PR is scoped to one issue or work item.
- Title: same structure as commits.
- Body: short and precise, enough to understand the change without extra explanation.
  - **What:** 1–3 bullets on what changed.
  - **Why:** one line, linking the brief (`docs/briefs/NNN-….md`) and the issue if there is one (`Closes #N`).
  - **Testing:** how it was verified (commands run, apps tried manually).
  - **Security:** boundaries touched and how they were checked, or `none` with a reason.
  - **Notes:** only if needed (breaking change, follow-up, decision recorded in CLAUDE.md).
