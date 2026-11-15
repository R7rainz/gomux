# gomux

A terminal multiplexer and session manager for developers, written in Go.

gomux keeps your shells and commands running in a background server, so you can close the terminal and come back to them later. It is built around projects: a `.gomux.toml` in a repo describes its panes and commands, and `gomux open .` brings them up, or reattaches if they're already running. It aims to work well with zero configuration and to stay small.

It is not a tmux replacement. tmux is everywhere and the right tool on remote servers; gomux targets the developer's own machine.

## Status

**Pre-alpha. Not usable yet.** The roadmap is in [CLAUDE.md](CLAUDE.md#9-roadmap).

## Install (planned)

Once v0.1 is released:

- Homebrew (macOS, Linux): `brew install R7rainz/tap/gomux`
- Arch Linux (AUR): `yay -S gomux-bin`
- Debian/Ubuntu, Fedora, Arch: `.deb`, `.rpm` and `.pkg.tar.zst` packages on the [releases page](https://github.com/R7rainz/gomux/releases)
- From source: `go install github.com/R7rainz/gomux/cmd/gomux@latest`

## License

[MIT](LICENSE)
