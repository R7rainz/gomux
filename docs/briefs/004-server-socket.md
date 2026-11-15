# 004 — Server and socket

Status: In progress
Phase: 1 (CLAUDE.md §9)
Branch: `feat/004-server-socket`. Feature work, so it gets a PR (CLAUDE.md §14); this brief is the PR's first commit.
Issue: none

Approved through the plan on 2026-11-15, with these decisions:
- one PR;
- `golang.org/x/sys` for the peer check;
- an unsafe socket directory is refused;
- on a version mismatch, `kill-server` signals the old server;
- PRs are merged with Rebase and merge.

## What it does

The first gomux you can run. A server listens on a private Unix socket, and two client commands talk to it over the item 003 protocol:

```
$ gomux ls
gomux: no server running on /var/folders/…/T/gomux-501/default.sock      (exit 1)
$ gomux server --foreground        # in another terminal; logs to stderr
$ gomux ls
no sessions
$ gomux kill-server                # server stops; socket file removed
$ gomux --version
gomux v0.0.0-…+dirty (protocol 1)
```

- `gomux server` without `--foreground` also runs in the foreground, but logs to `~/.local/state/gomux/server.log`.
- `server` is a hidden command: users never need it once autostart exists.

**After an upgrade**, a new gomux can meet an old server speaking a different protocol version.
- `gomux kill-server` then stops it with SIGTERM, using the server's process ID as reported by the kernel.
- Every other command explains the mismatch and points to `kill-server`.

**Out of scope:**
- **Autostart**, which comes with `gomux new` (item 007).
- **Sessions:** `ls` lists real sessions from item 008 on.
- **Config file and default keys:** config defaults arrive with the first config key.
- **Exiting when the last session ends:** there are no sessions yet, so the server runs until it is killed.

## Concepts

- **Unix domain sockets.**
  - A socket that lives at a path in the filesystem, used for talking between processes on one machine. The server *binds* and *listens* on the path; clients *connect* to it.
  - Who may connect is decided by filesystem permissions on the socket and the directory holding it.
  - Read more: `man 7 unix` (Linux), `man 4 unix` (macOS).
- **Peer credentials.**
  - The kernel can tell either end of a connected Unix socket the user ID (and process ID) of the process on the other end.
  - Because the kernel reports it, it can't be faked.
  - The calls: `SO_PEERCRED` on Linux; `LOCAL_PEERCRED` and `LOCAL_PEERPID` on macOS. `golang.org/x/sys/unix` wraps them.
  - The server uses this to reject other users; the client uses it to refuse an impostor server.
- **Socket directories and why `/tmp` is dangerous.**
  - On macOS, with no `XDG_RUNTIME_DIR`, the socket lives in `$TMPDIR/gomux-<uid>/`. `$TMPDIR` is per-user on macOS, but on Linux the fallback is `/tmp`, shared by everyone.
  - In a shared directory another user could pre-create `gomux-501`, or put a symlink there, and intercept or redirect us.
  - So we never follow a symlink (`Lstat`), require the directory to be owned by us, and refuse it if group or others have any access. This is what tmux does.
- **Stale sockets.**
  - If a server dies without cleaning up, the socket file stays behind.
  - Connecting to it fails with "connection refused"; binding a new socket at the same path fails with "address in use".
  - So before binding, we probe: a live server means "already running", a dead socket gets removed, and anything that isn't a socket is refused.
- **Path length limit.** A Unix socket path must fit in `sockaddr_un.sun_path`: 104 bytes on macOS, 108 on Linux, including a terminating zero. Long `$TMPDIR` or test paths can exceed it, so we check and suggest `GOMUX_SOCKET`.
- **XDG base directories.**
  - `$XDG_RUNTIME_DIR` (per-login runtime files, usually `/run/user/<uid>` on Linux) and `$XDG_STATE_HOME` (logs and state, default `~/.local/state`).
  - The spec says relative paths in these variables must be ignored.
  - Read more: the XDG Base Directory Specification.
- **Signals and graceful shutdown.**
  - SIGTERM and SIGINT ask a process to stop: we close the listener, close client connections, wait for our goroutines, and remove the socket.
  - SIGHUP is ignored, because a daemon shouldn't die when the terminal that started it closes.
  - Read more: `go doc os/signal.NotifyContext`.
- **The event loop.**
  - One goroutine owns all server state and processes requests one at a time from a channel. Each request carries its own reply channel.
  - This avoids locks on the state entirely (CLAUDE.md §6.3). In this item the state is just the empty session list, but every later feature plugs into the same loop.
- **Deadlines.** `net.Conn.SetReadDeadline` makes a blocked read fail after a time limit. The first frame must arrive within 5 s, so a connection that never speaks can't sit there forever.
- **Structured logging (`log/slog`).** Logs are `key=value` records with levels (debug, info, warn, error). We log events and message types, never message contents.

## Flows

### `gomux server [--foreground] [--log-level L]`

1. Pick the log level: the `--log-level` flag, else `GOMUX_LOG_LEVEL`, else `info`. Open the log: stderr with `--foreground`, otherwise `StateDir()/server.log` (directory 0700, file 0600; truncated at start if over 10 MB).
2. `config.SocketPath()`:
   - `$GOMUX_SOCKET` if set (the user's choice, so the directory isn't checked);
   - else `$XDG_RUNTIME_DIR/gomux/default.sock`;
   - else `$TMPDIR/gomux-<uid>/default.sock` (falling back to `/tmp`).
   - A path too long for the OS is an error.
3. `sock.Listen(path, ownDir)`:
   1. If gomux owns the directory, create it with mode 0700, or check the existing one: not a symlink, a directory, owned by us, no group or other access. Otherwise refuse with what's wrong and how to fix it.
   2. Handle whatever is at the socket path:
      - nothing → continue;
      - not a socket → refuse;
      - a socket that answers → "a gomux server is already running";
      - a refused connection → remove the stale file.
   3. Bind, then `chmod` the socket to 0600.
4. Ignore SIGHUP. SIGTERM and SIGINT cancel the server's context.
5. `server.Serve`: start the event loop, then accept connections until the context ends.
   - Each connection gets its own goroutine.
   - The process exits 0 after a clean shutdown.

### One client connection (server side)

1. Check the peer: its UID must equal ours, otherwise log a warning and close.
2. Read the first frame with a 5 s deadline:
   - not `hello` → close;
   - `version ≠ proto.Version` → reply `hello` with `version_mismatch` (giving both protocol versions and the server's binary version), then close;
   - otherwise reply `hello` with `server_version`.
3. Loop:
   1. `proto.Read`.
   2. Send `{msg, reply chan}` to the event loop.
   3. `proto.Write` the reply.
   4. If the reply was to `server.kill`, start shutdown.
   - Any read or write error closes the connection.

### Event loop

- `session.list` → `{"sessions":[]}`.
- `server.kill` → an empty reply, marked "shut down after sending".
- Anything else → `unknown_type`.

### Shutdown

1. Cancel the context.
2. Close the listener. Go removes the socket file when its own listener closes.
3. Close every client connection.
4. Wait for the connection goroutines.
5. Log "server stopped".

Because the listener closes before the connections, a client that waits for its connection to close knows the socket file is already gone.

### `gomux ls`

1. `client.Dial(path, version)`:
   1. `sock.Dial` connects. Nothing at the path, or connection refused → `ErrNoServer`.
   2. Check the server's UID; a mismatch means refuse.
   3. Do the `hello` handshake.
2. Send `session.list` and print `no sessions`, or one name per line.
   - Server-supplied text (names, error messages) is stripped of control characters before printing (CLAUDE.md §13).
- **No server:** `gomux: no server running on <path>`, exit 1.

### `gomux kill-server`

1. Dial, then send `server.kill`.
2. Read until the server closes the connection, which means the socket is gone.
3. Exit 0.

**Version mismatch:**
1. Take the server's PID (kernel-reported at connect time; the same-user check has already passed) and send it SIGTERM. That's the same graceful shutdown as above.
2. Wait up to 5 s for the socket to disappear, then print `stopped the old gomux server (pid N)`.
3. Every other command prints: `the running server (pid N) speaks protocol X, this gomux speaks Y; run 'gomux kill-server' to stop it (ends all its sessions)`.

### Error paths

| Situation | What happens |
|---|---|
| Unsafe socket directory | `gomux server` exits 1, naming the directory and the fix (`rm -rf` it or `chmod 700` it). |
| Server already running | `gomux server` exits 1 with the socket path. |
| Path too long | The command exits 1 and suggests a shorter `GOMUX_SOCKET`. |
| A malformed client frame | That connection closes; the server keeps running. |
| A client from another UID | Rejected and logged at warn. |
| A server owned by another UID | The client refuses to talk to it. |

## What is needed

- **Files:**
  - `internal/config/paths.go` (+ test)
  - `internal/sock/sock.go`, `peer_linux.go`, `peer_darwin.go` (+ test)
  - `internal/proto/proto.go` (+ test)
  - `internal/server/server.go` (+ test)
  - `internal/client/conn.go` (+ test)
  - `cmd/gomux/main.go` (+ e2e test)
- **Types and functions:**
  - `config.SocketPath() (path string, ownDir bool, err error)`
  - `config.StateDir() (string, error)`
  - `sock.Listen(path, ownDir) (*net.UnixListener, error)`
  - `sock.Dial(path) (*net.UnixConn, error)`
  - `sock.Peer(*net.UnixConn) (uid uint32, pid int, err error)`
  - `sock.ErrNoServer`
  - `server.Serve(ctx, ln, version, log) error`
  - `client.Dial(path, version) (*Conn, error)`
  - `(*Conn).Request(type, payload) (proto.Message, error)`
  - `client.VersionMismatchError{Message, PID}`
- **Protocol messages:**
  - `session.list`, which replies `{"sessions":[{"name":…}]}`; `SessionInfo` grows with item 008;
  - `server.kill`, with an empty reply;
  - the error code `unknown_type`.
- **Config keys:** none. Environment variables: `GOMUX_SOCKET`, `GOMUX_LOG_LEVEL`, `XDG_RUNTIME_DIR`, `XDG_STATE_HOME`, `TMPDIR`.
- **Dependencies:** `golang.org/x/sys`, the Go team's own pure-Go extension of `syscall`, already pulled in indirectly by Bubble Tea. It's needed because the standard library has no peer-credential call on macOS.

## Design

- **A new package, `internal/sock`,** keeps OS-specific syscalls out of `proto`. `proto` stays pure and fuzzable.
  - Dependency direction: `client → proto, sock, config`; `server → proto, sock`; `cmd → all`.
- **`$GOMUX_SOCKET` is used as given.** Environment variables are trusted (CLAUDE.md §13), and that path's directory is often `/tmp`, which our private-directory check would reject. The socket is still 0600 and peer-checked in both directions.
- **Refuse, don't repair, unsafe directories:** decided at approval. A directory someone else could write to may already contain a planted socket.
- **The kill reply is sent before shutdown starts.** The connection goroutine starts shutdown only after writing the `server.kill` reply, so the client always gets its answer.
- **Kill on version mismatch by PID:** decided at approval. The PID comes from the kernel while the connection is open, and only after the same-UID check.
  - **Residual risk:** if the server exited and its PID were reused by another process *of the same user* within milliseconds, that process would get SIGTERM. That's negligible and limited to the user's own processes.
- **The event loop exists from the start,** even though the state is empty, because CLAUDE.md §6.3 makes it the only owner of server state. Adding it later would mean rewriting every handler.
- **Rejected:**
  - autostart in this item: it would need sessions first (§6.1);
  - a umask change around `bind`: process-wide and racy; the 0700 directory and the peer check already cover the window before `chmod`;
  - FreeBSD peer code: not a tier-1 platform, and it would ship untested.

## Security

**Trust boundary: the socket** (CLAUDE.md §13).
- Private-directory checks: `Lstat` (no symlinks), owner equals our UID, no group or other permissions; otherwise refuse.
- Stale files are removed only if they are sockets and refuse connections.
- The socket is 0600, and the peer UID is checked in **both** directions.
- First-frame deadline of 5 s. Frames are bounded by `proto.MaxFrame` (item 003).
- Only message *types* are logged, never payloads. The log is 0600 in a 0700 directory.
- **New this item:** the client strips control characters from server-supplied text (error messages, session names) before printing, closing the item tracked in 003's security review.
- SIGTERM goes only to a kernel-reported PID that passed the same-UID check.
- `govulncheck` covers `x/sys`, and `/security-review` runs before the PR.

## Test plan

Socket tests use `os.MkdirTemp("/tmp", "gmx")`, never `t.TempDir()`, because of the macOS 104-byte path limit.

- **config:**
  - environment precedence: `GOMUX_SOCKET` > `XDG_RUNTIME_DIR` > `TMPDIR` > `/tmp`;
  - relative XDG paths ignored; UID in the fallback name; too-long path → error;
  - state-dir fallbacks.
- **sock:**
  - directories: a new one is created with 0700; an existing private one is accepted; 0755 is refused; a symlink is refused; another owner is refused (simulated by checking against a different UID);
  - socket paths: a stale socket is removed; a live server → already running; a regular file at the path is refused;
  - the socket's mode is 0600; `Peer` returns our UID and the right PID;
  - `Dial` with nothing there → `ErrNoServer`.
- **proto:** the `session.list` reply encodes as `{"sessions":[]}`, not `null`.
- **server** (in-process, raw `proto` clients):
  - handshake succeeds; mismatch → error, then closed; a non-`hello` first frame → closed; silence → closed after the deadline (shortened in the test);
  - `unknown_type`; `session.list` returns empty;
  - `server.kill` → reply, then shutdown, socket removed, `Serve` returns; context cancel → same cleanup;
  - 10 concurrent clients under `-race`.
- **client:**
  - `ErrNoServer`; `VersionMismatchError` with the PID;
  - request IDs increase and events are skipped while waiting for a reply;
  - control characters are stripped.
  - These run against a fake server built from `sock` and `proto`.
- **cmd (end to end, built binary, `GOMUX_SOCKET` in a temp dir):**
  1. `ls` with no server → exit 1 and the message.
  2. `server --foreground` running → `ls` prints `no sessions` → a second `server` fails with "already running".
  3. `kill-server` → exit 0; the server process exits 0; the socket is gone.
  4. Version mismatch: a fake old server answers `version_mismatch` → `kill-server` sends SIGTERM to it and reports.
  5. `--version` prints the protocol.
- **Manual:** the five commands in "What it does", in your terminal.

## Done when

- [ ] All packages and tests above, passing under `go test -race ./...`; vet, staticcheck and govulncheck clean
- [ ] End-to-end test green locally and in CI (ubuntu and macOS)
- [ ] CLAUDE.md updated:
  - §2 current state;
  - §5 CLI;
  - §6.1 socket rules, client peer check, mismatch kill;
  - §6.2 `internal/sock`;
  - §7 `x/sys`;
  - §8 FreeBSD note;
  - §9 Phase 1 ✓
- [ ] `/security-review` run on the branch, with no unresolved findings (CLAUDE.md §13)
- [ ] PR opened with a Security line

## Open questions

None.

## Notes after implementation

<!-- Deviations from this brief, and why. -->
