// Command gomux is a terminal multiplexer and session manager for developers.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/R7rainz/gomux/internal/client"
	"github.com/R7rainz/gomux/internal/config"
	"github.com/R7rainz/gomux/internal/proto"
	"github.com/R7rainz/gomux/internal/server"
	"github.com/R7rainz/gomux/internal/sock"
)

// version is set by release builds: go build -ldflags "-X main.version=v1.2.3".
var version string

// `gomux server` is deliberately missing: users never need to start it by hand
// once autostart exists (item 007).
const usage = `usage: gomux <command>

commands:
  ls            list sessions
  kill-server   stop the server and every session
  --version     print the version
`

// errUsage means the flag package has already explained the problem.
var errUsage = errors.New("usage")

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	commands := map[string]func(args []string, stdout io.Writer) error{
		"ls":          cmdLs,
		"kill-server": cmdKillServer,
		"server":      cmdServer,
	}
	switch args[0] {
	case "--version", "-version":
		fmt.Fprintf(stdout, "gomux %s (protocol %d)\n", versionString(), proto.Version)
		return 0
	case "--help", "-help", "-h", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "gomux: unknown command %q\n%s", args[0], usage)
		return 2
	}
	if err := cmd(args[1:], stdout); err != nil {
		if errors.Is(err, errUsage) {
			return 2
		}
		fmt.Fprintln(stderr, "gomux:", err)
		return 1
	}
	return 0
}

func cmdLs(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return errors.New("ls takes no arguments")
	}
	path, _, err := config.SocketPath()
	if err != nil {
		return err
	}
	conn, err := client.Dial(path, versionString())
	if err != nil {
		return dialError(err, path)
	}
	defer conn.Close()
	reply, err := conn.Request(proto.TypeSessionList, nil)
	if err != nil {
		return err
	}
	var list proto.SessionList
	if err := json.Unmarshal(reply.Payload, &list); err != nil {
		return err
	}
	if len(list.Sessions) == 0 {
		fmt.Fprintln(stdout, "no sessions")
	}
	for _, s := range list.Sessions {
		fmt.Fprintln(stdout, client.Printable(s.Name))
	}
	return nil
}

func cmdKillServer(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return errors.New("kill-server takes no arguments")
	}
	path, _, err := config.SocketPath()
	if err != nil {
		return err
	}
	conn, err := client.Dial(path, versionString())
	if vm, ok := errors.AsType[*client.VersionMismatchError](err); ok {
		return stopOldServer(vm.PID, path, stdout)
	}
	if err != nil {
		return dialError(err, path)
	}
	defer conn.Close()
	if _, err := conn.Request(proto.TypeServerKill, nil); err != nil {
		return err
	}
	return conn.Wait() // the server closes our connection only after removing its socket
}

// stopOldServer stops a server from another protocol version, which can't be
// asked over the socket, with SIGTERM (its graceful shutdown). pid comes from
// the kernel and was checked to belong to this user when we connected.
func stopOldServer(pid int, path string, stdout io.Writer) error {
	if pid <= 1 { // kill(0) or kill(-1) would signal whole process groups
		return fmt.Errorf("can't stop the old server: the kernel reported pid %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stopping the old server (pid %d): %w", pid, err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(stdout, "stopped the old gomux server (pid %d)\n", pid)
			return nil
		}
	}
	return fmt.Errorf("sent SIGTERM to the old server (pid %d), but it is still running", pid)
}

func dialError(err error, path string) error {
	if errors.Is(err, client.ErrNoServer) {
		return fmt.Errorf("no server running on %s", path)
	}
	return err
}

func cmdServer(args []string, _ io.Writer) error {
	flags := flag.NewFlagSet("gomux server", flag.ContinueOnError)
	foreground := flags.Bool("foreground", false, "log to stderr instead of the log file")
	level := flags.String("log-level", cmp.Or(os.Getenv("GOMUX_LOG_LEVEL"), "info"), "debug, info, warn or error")
	if err := flags.Parse(args); err != nil {
		return errUsage
	}
	if flags.NArg() > 0 {
		return errors.New("server takes no arguments")
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*level)); err != nil {
		return fmt.Errorf("bad log level %q: use debug, info, warn or error", *level)
	}
	log, closeLog, err := openLog(*foreground, lvl)
	if err != nil {
		return err
	}
	defer closeLog()

	path, ownDir, err := config.SocketPath()
	if err != nil {
		return err
	}
	ln, err := sock.Listen(path, ownDir)
	if err != nil {
		return err
	}
	handleHangups()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("server started", "socket", path, "pid", os.Getpid(), "version", versionString(), "protocol", proto.Version)
	return server.Serve(ctx, ln, versionString(), log)
}

// handleHangups lets the server outlive the terminal that started it. It
// catches and discards SIGHUP rather than ignoring it: an ignored signal stays
// ignored in every program the server starts, and pane programs must still
// die from Term.Kill's hang-up (CLAUDE.md §6.6).
func handleHangups() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGHUP)
}

// openLog returns the server's logger: stderr with --foreground, otherwise
// server.log in the state directory (0600 in a 0700 directory).
func openLog(foreground bool, lvl slog.Level) (*slog.Logger, func(), error) {
	opts := &slog.HandlerOptions{Level: lvl}
	if foreground {
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), func() {}, nil
	}
	dir, err := config.StateDir()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "server.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(path); err == nil && fi.Size() > 10<<20 {
		flags |= os.O_TRUNC // shortcut: truncated at start when over 10 MB; add rotation if anyone needs history
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := f.Chmod(0o600); err != nil { // in case an older file was looser
		f.Close()
		return nil, nil, err
	}
	return slog.New(slog.NewTextHandler(f, opts)), func() { f.Close() }, nil
}

// versionString prefers the release-stamped version, then the module version
// Go embeds (go install …@vX, or git-derived for local builds), then "dev".
func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
