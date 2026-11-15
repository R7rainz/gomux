# 003 — Wire protocol

Status: In progress
Phase: 1 (CLAUDE.md §9)
Branch: `feat/003-wire-protocol`. Feature work, so it gets a PR (CLAUDE.md §14); this brief is the PR's first commit.
Issue: none

## What it does

Adds `internal/proto`, the package that lets a gomux client and the gomux server exchange messages over a Unix socket.
- It turns a message into bytes on the wire and back, and rejects anything malformed or oversized.
- It defines the one message every connection starts with: the `hello` handshake that checks both sides speak the same protocol version.

Nothing user-visible changes yet. The server and client that use this package arrive in item 004. This item is the vocabulary and grammar they'll speak.

**Out of scope:**
- Sockets, the server and the client (item 004).
- Message types for features that don't exist yet: `session.list` and `server.kill` arrive with 004, input and screen updates with 006/007.
- The terminal size and environment in `hello`, which arrive with `gomux new` (007), the first command that needs them.

## Concepts

- **Byte streams have no message boundaries.**
  - A Unix stream socket, like TCP, delivers a continuous stream of bytes.
  - If the client sends two messages, the server may receive them glued together in one read, or one message split across several reads.
  - So every protocol needs **framing**: a rule for where one message ends and the next begins.
  - Read more: `man 7 unix` (Linux), `man 4 unix` (macOS), or any explanation of TCP "message framing".
- **Length-prefix framing.** Each message is sent as a 4-byte length followed by exactly that many bytes of body.
  - The reader always knows how much to read next.
  - It can refuse an oversized message before reading or allocating it.
  - The body can later be something other than JSON (CLAUDE.md §6.8 has a `shortcut:` for binary screen updates).
  - The alternative, one JSON object per line (NDJSON), works too, since JSON escapes newlines. But the reader can't know a message's size until it has scanned for the newline.
- **Big-endian ("network byte order").** The 4-byte length is written most-significant byte first, the convention for wire formats. Go's `encoding/binary.BigEndian` does it. Read more: `go doc encoding/binary`.
- **`io.ReadFull`.** A single `Read` may return fewer bytes than asked for. `io.ReadFull` keeps reading until it has exactly n bytes, or reports `io.ErrUnexpectedEOF` if the stream ends mid-message. Read more: `go doc io.ReadFull`.
- **An envelope with a deferred payload.**
  - Every message has the same outer shape: `version`, `type`, `request_id`, and a `payload` (or an `error`).
  - The payload is kept as `json.RawMessage`, still-encoded bytes, until the code knows the `type` and can decode it into the right Go struct.
  - Read more: `go doc encoding/json.RawMessage`.
- **Requests, responses, events.**
  - A **request** carries a `request_id` chosen by the sender.
  - The **response** repeats that `request_id`, so the client can match answers to questions even if they arrive out of order.
  - An **event** is something the server announces unprompted, like a screen update, and has no `request_id`.
- **Protocol versioning and the handshake.**
  - After a `brew upgrade`, a new client can meet an old server that's still running (CLAUDE.md §6.1).
  - The first message on every connection says which protocol version the client speaks. A server that speaks a different one answers with a clear error and hangs up, instead of misreading every message after it.
- **Fuzzing.**
  - Go's built-in fuzzer (`go test -fuzz`) generates millions of random and mutated inputs to find crashes.
  - Our decoder reads bytes from other processes, so it is a **trust boundary** (CLAUDE.md §13) and must never panic or allocate huge memory on bad input.
  - Seed inputs are added in the test code with `f.Add`, and plain `go test` replays them, so CI always checks the known cases.
  - Inputs the fuzzer explores go to the Go build cache. Only an input that makes the test fail is written to `testdata/fuzz/FuzzRead/`, and it gets committed as a regression test.
  - Read more: <https://go.dev/doc/security/fuzz>.

## Flows

### Writing a message (`proto.Write`)

1. Encode the `Message` as JSON with HTML escaping off. By default Go writes `<`, `>` and `&` as 6-byte escapes; that only bloats pasted text and could push a re-encoded frame over the limit.
2. If the body is larger than `MaxFrame` (16 MiB), return `ErrFrameTooLarge`. Never send what the other side would reject.
3. Build `[4-byte big-endian length][body]` in one buffer and write it with **one** `Write` call. A frame is never interleaved with another, even if a caller shares a writer by mistake.

### Reading a message (`proto.Read`)

1. `io.ReadFull` 4 bytes.
   - `io.EOF` here means the peer closed cleanly between messages, and is returned as is.
2. Decode the length `n`. If `n == 0` or `n > MaxFrame`, return an error **before allocating anything**.
3. Allocate `n` bytes and `io.ReadFull` the body. If the stream ends early, return `io.ErrUnexpectedEOF`.
4. Unmarshal the body into `Message`. The body must be a JSON object with a non-empty `type`; otherwise return an error.
5. Unknown fields are ignored, so adding optional fields later doesn't break older peers within the same version.

### Handshake (defined here; carried out by item 004's client and server)

1. **Client → server**, first frame: `{"version":1,"type":"hello","request_id":"1","payload":{"client_version":"v0.1.0"}}`.
2. **Server:**
   - If `version` equals its own `proto.Version`, it replies `{"version":1,"type":"hello","request_id":"1","payload":{"server_version":"v0.1.0"}}`.
   - Otherwise it replies `{"version":1,"type":"hello","request_id":"1","error":{"code":"version_mismatch","message":"server speaks protocol 1, client speaks 2"}}` and closes the connection.
3. **A first frame that isn't `hello`** is a protocol violation, and the server closes the connection.

### Error paths

Any decode error (oversized, truncated, malformed JSON, missing `type`) means the stream can no longer be trusted. The caller closes the connection and **does not try to resync**, because after a bad length there's no way to find the next frame boundary.

### Shutdown and cleanup

None. The package holds no state, goroutines or files; callers own their connections.

## What is needed

- **Files:**
  - `internal/proto/proto.go`
  - `internal/proto/proto_test.go`
  - `internal/proto/fuzz_test.go`, a separate file so the fuzz test gets its own commit.
- **Types and functions:**
  - `const Version = 1`, the protocol version, bumped on any incompatible change once v0.1.0 ships. Before then nothing is promised, but bumping is still the habit.
  - `const MaxFrame = 16 << 20`
  - `var ErrFrameTooLarge`
  - `type Message struct { Version int; Type string; RequestID string; Payload json.RawMessage; Error *Error }`, with JSON tags `version`, `type`, `request_id,omitempty`, `payload,omitempty` and `error,omitempty`.
  - `type Error struct { Code, Message string }`, which implements `error`, so a response error can be returned directly.
  - `func Write(w io.Writer, m Message) error` and `func Read(r io.Reader) (Message, error)`.
  - The handshake: `const TypeHello = "hello"`, `type Hello struct { ClientVersion string }`, `type HelloReply struct { ServerVersion string }`, `const CodeVersionMismatch = "version_mismatch"`.
- **Protocol messages:** only `hello` (above).
- **Config keys:** none.
- **Dependencies:** none. Stdlib only: `encoding/json`, `encoding/binary`, `io`, `errors`.
- **CLAUDE.md §6.8**, updated to match two small decisions in Design:
  - every response reuses its request's `type`;
  - the protocol version lives in the envelope, so `hello`'s payload doesn't repeat it.

## Design

- **Responses reuse the request's `type`** (`hello` → `hello`, `session.list` → `session.list`). CLAUDE.md currently says the server answers `hello` with `welcome`. One rule, "a response has the request's `type` and `request_id` plus a payload or an error", is simpler than a special name per reply. **This changes a recorded decision; approving the brief approves it.**
- **The version lives in the envelope**, on every message as the spec showed. In `hello` it *is* the client's protocol version, so the payload carries only the binary version (`client_version`, shown in error messages). After the handshake both sides know the version is equal; the field costs about 14 bytes per message and lets the decoder reject a stray frame from the wrong version.
- **Length prefix, not NDJSON:** decided in CLAUDE.md §6.8, and reasoned in Concepts.
- **Payload as `json.RawMessage`:** the envelope decodes once, and the payload decodes into a concrete struct only when its type is known. There's no `interface{}` guessing.
- **No `DisallowUnknownFields`:** within one protocol version, new optional fields must not break older peers. Incompatible changes bump `Version` instead.
- **No helpers beyond `Read` and `Write`** until two callers need the same one (CLAUDE.md §4). Callers `json.Marshal` their payloads.
- **Decided at approval:**
  - replies reuse the request's `type`;
  - the version goes in every envelope;
  - unknown fields are ignored;
  - for feature work, the brief is committed inside its PR (recorded in CLAUDE.md §12).
- **Rejected:** gRPC/Protobuf (CLAUDE.md §4, out of scope), `encoding/gob` (Go-only, opaque to debugging), and a per-connection `Codec` type (no state worth holding yet).

## Security

**Trust boundary: the socket** (CLAUDE.md §13).
- Only same-user processes can connect (item 004's peer check), but a buggy or malicious local program can still send garbage. `Read` must survive any byte sequence:
  - no panics;
  - no allocation before the length is checked against `MaxFrame`, so a 4 GB length header can't exhaust memory;
  - every error ends the connection.
- **The fuzz test enforces this.** Its seeds replay in every CI run, and a longer fuzz session is part of the end-of-phase security check.
- **Payloads will carry keystrokes and screen contents** in later items. This package never logs messages, and its errors never include payload bytes, only sizes and type names, so nothing sensitive leaks into logs (CLAUDE.md §4).

## Test plan

- **Unit** (table-driven):
  - round trip of a request, a response with a payload, an error response and an event;
  - several frames written back to back and read back in order from one stream;
  - an oversized header is rejected without reading the body;
  - `Write` refuses an oversized body;
  - zero length, a truncated header, a truncated body (`ErrUnexpectedEOF`), clean EOF between frames (`io.EOF`);
  - invalid JSON, a JSON non-object (`[]`, `"x"`, `null`), a missing `type`;
  - unknown fields are ignored;
  - `<&>` survives unescaped;
  - `*Error` satisfies `error` with a readable message;
  - the `hello`, `HelloReply` and `version_mismatch` messages encode to exactly the JSON shown in Flows.
- **Fuzz:** `FuzzRead`, seeded with `f.Add` (valid frames, an oversized header, truncated frames, garbage). Properties:
  - `Read` never panics;
  - if `Read` succeeds, `Write` → `Read` → `Write` gives identical bytes (re-encoding is stable).
  - Run locally with `go test ./internal/proto -fuzz FuzzRead -fuzztime 30s`, plus a 2-minute run for this first trust-boundary code. CI replays the seeds.
- **Manual:** none. Nothing is user-visible yet.

## Done when

- [ ] `internal/proto` with `Read`, `Write`, the envelope, the error type and the `hello` types
- [ ] Unit tests and the fuzz test (seeds via `f.Add`), all passing under `go test -race ./...`
- [ ] 30 s and 2 min local fuzz runs find nothing
- [ ] `/security-review` run on the branch, with no unresolved findings
- [ ] `go vet`, staticcheck and `govulncheck ./...` clean
- [ ] CLAUDE.md §6.8 updated (response `type` rule, version in the envelope, `hello` payload)
- [ ] Security checks for this item done (CLAUDE.md §13); PR prepared with a Security line

## Open questions

None.

## Notes after implementation

<!-- Deviations from this brief, and why. -->
