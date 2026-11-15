# 001 — Repo setup

Status: Done
Phase: 0 (CLAUDE.md §9)
Branch: none — setup work is committed straight to `main` (CLAUDE.md §14)
Issue: none

## What it does

After this lands, the repo has a license, a short README and a real binary entry point at `cmd/gomux/`:
- `go install github.com/R7rainz/gomux/cmd/gomux@latest` installs a binary named `gomux`.
- `gomux --version` prints the version.
- Every push to `main`, every PR, and a weekly schedule run the checks: vet, staticcheck, race-enabled tests and a vulnerability scan, on Linux and macOS.
- A packaging job proves on every run that GoReleaser can still build all release artifacts.

**Out of scope:**
- Any multiplexer functionality. `gomux` without `--version` prints usage and exits.
- Publishing anywhere. No Homebrew tap, no AUR and no GitHub Release until item 011, which needs secrets and the name-availability check.
- Man page and shell completions. They need real subcommands first.

## Concepts

- **The `cmd/<name>/` convention.** `go install` and `go build` name the binary after the last element of the package path. Today `main` lives in `cmd/`, so the binary would be called `cmd`. Moving it to `cmd/gomux/` makes it `gomux`. Read more: `go help install`, <https://go.dev/doc/modules/layout>.
- **Build-time variables (`-ldflags -X`).** The linker can overwrite a package-level string variable at build time: `go build -ldflags "-X main.version=v0.1.0"`. This is how release builds stamp their version without a version file. Read more: `go help build`, `go tool link` (`-X` flag).
- **Build info (`runtime/debug.ReadBuildInfo`).** Every Go binary embeds its module version.
  - `go install …@v0.1.0` records `v0.1.0`.
  - A local `go build` (Go 1.24+) records a version derived from git, like `v0.0.0-20261115…-abc123+dirty`.
  - We use it when no `-X` version was given. Read more: <https://pkg.go.dev/runtime/debug#ReadBuildInfo>.
- **`CGO_ENABLED=0` and cross-compiling.** With cgo off, Go produces a fully static binary and can build for any OS/CPU from any machine by setting `GOOS`/`GOARCH`. That is what makes one CI job able to build Linux and macOS packages. Read more: `go help environment`.
- **Static checks.**
  - **`go vet`** catches suspicious code (wrong printf verbs, copied locks).
  - **`staticcheck`** catches bugs and dead code that vet doesn't.
  - **The race detector (`go test -race`)** catches data races at runtime. It is essential for our concurrent server.
  - **`govulncheck`** reports known vulnerabilities only in code paths we actually call.
  - Read more: `go doc cmd/vet`, <https://staticcheck.dev>, <https://go.dev/doc/articles/race_detector>, <https://go.dev/doc/security/vuln>.
- **GitHub Actions.** A workflow file in `.github/workflows/` lists **triggers** (push, pull_request, schedule, manual) and **jobs**. A job runs on a fresh VM (the **runner**); a **matrix** runs the same job on several OSes.
  - Each run gets a `GITHUB_TOKEN`; the `permissions:` block limits what it can do.
  - A third-party action referenced by tag (`@v4`) can be silently re-pointed by its owner. Referencing it by full commit SHA cannot.
  - Read more: <https://docs.github.com/actions>, "Security hardening for GitHub Actions".
- **GoReleaser and nFPM.** GoReleaser reads one `.goreleaser.yaml` and produces archives, checksums and, via its bundled nFPM, Linux packages (`.deb`, `.rpm`, `.apk`, Arch `.pkg.tar.zst`). `--snapshot` builds everything locally without publishing, which is how CI proves the config still works. Read more: <https://goreleaser.com>, <https://nfpm.goreleaser.com>.
- **MIT license.** Anyone may use, copy, modify and redistribute the code, including commercially, as long as the copyright notice stays. No warranty. Package managers require an OSI license before they accept a package.

## Flows

### Version lookup (`gomux --version`)

1. `main` parses flags with stdlib `flag`, which accepts both `-version` and `--version`.
2. If `main.version` is non-empty (set by `-ldflags -X` in release builds), print it.
3. Else read `debug.ReadBuildInfo()`. If `Main.Version` is set and not `(devel)`, print it (this covers `go install …@vX` and local git builds).
4. Else print `dev`.
5. Output format: `gomux <version>`, exit 0.

**Any other invocation** prints `usage: gomux --version` to stderr and exits 2. That usage line is honest about the only thing the binary can do today, and later items grow it.

### Developer change → CI

1. I commit locally; you push to `main` (or push a branch and open a PR).
2. GitHub starts the `ci` workflow. The **checks** job runs twice in parallel (ubuntu-latest, macos-latest):
   - check out the code;
   - `setup-go` installs the Go version named in `go.mod`;
   - `go vet ./...`, `staticcheck ./...`, `go test -race ./...` and `govulncheck ./...` run in turn.
   - Any failure fails the job and marks the commit red.
3. The **release-snapshot** job (ubuntu only) runs `goreleaser release --snapshot --clean`. Artifacts are built in `dist/` and discarded with the VM.
4. You see ✓/✗ next to the commit on GitHub. Failures email the pusher (GitHub default).

### Weekly vulnerability scan

1. A `schedule` cron (Mondays 06:00 UTC) runs the same workflow on `main`. No code changed, but the vulnerability database may have new entries.
2. If `govulncheck` finds something new, the run fails. GitHub emails the user who last changed the workflow's cron line.

### Error paths

- **Tool version doesn't support Go 1.27** (staticcheck releases track Go releases) → the checks job fails at that step → pin a newer tool version. Pins live in one place in the workflow.
- **GoReleaser config breaks** (new deprecation, bad field) → the snapshot job fails while the checks job stays green. Fix the config in its own commit.
- **macOS-only or Linux-only failure** → the matrix shows which OS. Both must pass.

### Shutdown and cleanup

Nothing long-running. CI VMs are discarded after each job. `dist/` and `bin/` are git-ignored, so local builds never get committed.

## What is needed

- **Packages/files:**
  - `LICENSE`: MIT, "Copyright (c) 2026 R7rainz".
  - `README.md`:
    - one paragraph on what gomux is;
    - status: pre-alpha, not usable yet;
    - planned install channels (Homebrew tap, AUR, `.deb`/`.rpm`/`.pkg.tar.zst`, `go install`);
    - license.
  - `cmd/main.go` → `cmd/gomux/main.go`, rewritten for `--version`.
  - `.gitignore`: `bin/`, `dist/`.
  - `.github/workflows/ci.yml`: the `checks` matrix job and the `release-snapshot` job; triggers `push` (main), `pull_request`, `schedule` (weekly), `workflow_dispatch`.
  - `.goreleaser.yaml` (GoReleaser v2 schema):
    - one build: `./cmd/gomux`, `CGO_ENABLED=0`, linux/darwin × amd64/arm64, `-s -w -X main.version={{.Version}}`;
    - tar.gz archives with LICENSE and README;
    - `checksums.txt`;
    - `nfpms` with formats `deb`, `rpm`, `apk`, `archlinux`;
    - no publishers.
  - `CLAUDE.md`:
    - §2 current state;
    - §3, drop the "until then use `./cmd`" note;
    - §10 and §11, license resolved as MIT;
    - §6.1 and §9, the autostart fix approved in the plan: only `gomux`, `new` and `open` autostart the server; `ls`, `attach`, `kill` and `kill-server` say "no server running"; Phase 1 starts the server by hand; autostart moves to item 007.
- **Types and functions:** in `cmd/gomux/main.go`, `var version string` and `func versionString() string`. Nothing else.
- **Protocol messages:** none.
- **Config keys:** none.
- **Dependencies:** none added to `go.mod`. Build-time tools are pinned to exact versions in the workflow, not in `go.mod`: `staticcheck` and `govulncheck` via `go run <module>@<version>`, GoReleaser via its action.
- **GitHub Actions used:** `actions/checkout`, `actions/setup-go`, `goreleaser/goreleaser-action`, each pinned by commit SHA with the version in a trailing comment.

## Design

- **stdlib `flag`, not cobra:** decided in CLAUDE.md §7. One flag today.
- **`staticcheck` rather than golangci-lint:** one focused tool, no config file, decided in §7.
- **`go run tool@version` in CI** instead of `go install` plus caching: one line per tool, exact pin, no PATH setup. The cost is a few seconds of compile, which is cached by `setup-go`'s module/build cache.
- **Snapshot packaging on every run**, not only at release time. Packaging configs rot silently; a broken one discovered on release day blocks the release. About 1–2 minutes per run.
- **CI triggers:** `push` is limited to `main`, plus `pull_request`, so a PR branch isn't checked twice per push.
- **Version source:** `-X` first, build info second, `dev` last. There is no version file to forget to bump; the git tag is the single source of truth.
- **Publishers (Homebrew tap, AUR) deferred to 011:** they need secrets, an AUR account and the name check, and publishing before anything works would mislead users.

## Security

Runtime trust boundaries (CLAUDE.md §13): none touched. The binary only prints its version; no socket, files, pane output or external commands.

Supply chain, which this item does touch:
- Third-party actions are pinned by commit SHA, so a compromised or re-tagged action can't change what runs.
- The workflow sets `permissions: contents: read`, so the token can't push, create releases or touch issues.
- No secrets are used.
- No `pull_request_target` trigger. PRs from forks run with read-only rights and no secrets.
- `govulncheck` starts gating every change and runs weekly.

## Test plan

- **Unit:** none. `versionString` is three branches over values only a real build can set; the build checks below cover it.
- **Local checks:**
  - `go build ./... && go vet ./... && go test -race ./...`
  - `go run honnef.co/go/tools/cmd/staticcheck@<pinned> ./...`
  - `go run golang.org/x/vuln/cmd/govulncheck@<pinned> ./...`
- **Version flow:**
  - `go build -o bin/gomux ./cmd/gomux && bin/gomux --version` prints a git-derived version.
  - With `-ldflags "-X main.version=v9.9.9"` it prints `gomux v9.9.9`.
  - `bin/gomux` with no flags prints usage and exits 2.
- **Binary name:** `go list -f '{{.Target}}' ./cmd/gomux` ends in `/gomux`.
- **Packaging:** `goreleaser check`, and if GoReleaser is installed locally, `goreleaser release --snapshot --clean`. `dist/` must contain 4 archives, `checksums.txt` and `.deb`/`.rpm`/`.apk`/`.pkg.tar.zst` for each Linux arch.
- **CI:** after you push, both matrix legs and the snapshot job are green.

## Done when

- [x] LICENSE, README, `.gitignore` committed
- [x] `main` lives at `cmd/gomux/` and `--version` works as described in Flows
- [x] CI workflow green on ubuntu and macOS, snapshot job green (run 43717595675)
- [x] CLAUDE.md updated (§2, §3, §6.1, §9, §10, §11)
- [x] `go test -race ./...` and `govulncheck ./...` pass
- [x] Security checks for this item done (CLAUDE.md §13)

## Open questions

None left. The copyright holder defaulted to `R7rainz`; change the LICENSE line if you want your real name.

## Notes after implementation

**Pinned versions:**
- `actions/checkout` v7.0.1, `actions/setup-go` v7.0.0, `goreleaser/goreleaser-action` v7.3.0, each pinned by commit SHA;
- GoReleaser v2.20.2, staticcheck v0.8.2, govulncheck v1.9.0.

**Local verification:**
- vet, `test -race`, staticcheck and govulncheck all pass. govulncheck reports "No vulnerabilities found."
- Every `--version` and usage path behaves as described in Flows. A local build prints `gomux v0.0.0-<date>-<commit>+dirty`, and `-X main.version=v9.9.9` prints `gomux v9.9.9`.
- `go list` target ends in `/gomux`.
- `goreleaser check` passes with no deprecation warnings.
- A snapshot build produced 4 archives, `checksums.txt`, and `.deb`/`.rpm`/`.apk`/`.pkg.tar.zst` packages for amd64 and arm64. The packaged binary reports `0.0.0-SNAPSHOT-<commit>`.

**Deviations:**
- `.gitignore` also ignores `/gomux`, because `go build ./...` with a single main package writes the binary into the repo root.
- The `.deb` additionally ships the license as `/usr/share/doc/gomux/copyright` (the Debian convention in CLAUDE.md §10). Every format keeps `/usr/share/licenses/gomux/LICENSE`.
- `CLAUDE.md` §9 also gained the prototype-spike step (approved in the plan) as the fourth ⚠ change. The spike-related references in §6.7, §7 and §11 now point to item 002, and §11 dropped the resolved license item.
