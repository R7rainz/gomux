// Package term runs one program on a pseudo-terminal and manages its whole
// life: input, output, size, exit and kill (CLAUDE.md §6.6).
package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

var (
	// killGrace is how long Kill waits after SIGHUP before sending SIGKILL.
	killGrace = 2 * time.Second
	// drainTimeout is how long the program's last output may take to arrive
	// after it exits, before the Term counts as done.
	drainTimeout = 200 * time.Millisecond
	// maxPending caps input queued but not yet written to the program.
	maxPending = 1 << 20
)

// ErrExited is returned by Write and Resize once the program has exited.
var ErrExited = errors.New("term: program has exited")

// Spec describes a program to run on a new PTY.
type Spec struct {
	Path       string   // executable; looked up in PATH if it has no slash
	Args       []string // argv including argv[0], e.g. "-zsh" for a login shell; nil means [Path]
	Dir        string
	Env        []string // nil means this process's environment
	Cols, Rows int
	Output     io.Writer // receives everything the program writes, from one goroutine
}

// Term is one program running on a PTY.
type Term struct {
	cmd        *exec.Cmd
	ptmx       *os.File // the PTY master
	done       chan struct{}
	readerDone chan struct{}
	wake       chan struct{} // capacity 1: input is pending

	mu      sync.Mutex
	pending []byte // input not yet handed to the writer goroutine
	reaped  bool   // the process has been reaped; its PID must not be signalled
	exited  bool   // the Term is finished; Write and Resize fail
}

// Start runs the program described by s on a new PTY of the given size.
func Start(s Spec) (*Term, error) {
	if err := checkSize(s.Cols, s.Rows); err != nil {
		return nil, err
	}
	if s.Output == nil {
		return nil, errors.New("term: Spec.Output is required")
	}
	cmd := exec.Command(s.Path)
	if s.Args != nil {
		cmd.Args = s.Args
	}
	cmd.Dir, cmd.Env = s.Dir, s.Env
	// StartWithSize runs the child with setsid and the PTY as its controlling
	// terminal, so its process-group ID equals its PID.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(s.Cols), Rows: uint16(s.Rows)})
	if err != nil {
		return nil, fmt.Errorf("term: start %s: %w", s.Path, err)
	}
	t := &Term{
		cmd:        cmd,
		ptmx:       ptmx,
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
		wake:       make(chan struct{}, 1),
	}
	go t.read(s.Output)
	go t.write()
	go t.wait()
	return t, nil
}

// Pid is the program's process ID, which is also its process-group ID.
func (t *Term) Pid() int { return t.cmd.Process.Pid }

// Done is closed once the program has exited, been reaped, and its output
// has been drained (or drainTimeout passed, if a background job still holds
// the PTY open).
func (t *Term) Done() <-chan struct{} { return t.done }

// ExitState reports how the program ended. Valid after Done is closed.
func (t *Term) ExitState() *os.ProcessState { return t.cmd.ProcessState }

// Write queues p as input to the program. It never blocks; it fails if more
// than maxPending bytes are already waiting, so a program that stops reading
// its input can only stall itself.
func (t *Term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exited {
		return 0, ErrExited
	}
	if len(t.pending)+len(p) > maxPending {
		return 0, errors.New("term: input queue full; the program isn't reading its input")
	}
	t.pending = append(t.pending, p...)
	select {
	case t.wake <- struct{}{}:
	default: // the writer is already due to run
	}
	return len(p), nil
}

// Resize sets the PTY's size; the kernel sends SIGWINCH to the foreground job.
func (t *Term) Resize(cols, rows int) error {
	if err := checkSize(cols, rows); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exited {
		return ErrExited
	}
	return pty.Setsize(t.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Kill hangs up the program's process group (SIGHUP, as closing a terminal
// window does), sends SIGKILL if it is still running after killGrace, and
// returns once Done is closed. It is safe to call repeatedly and concurrently.
func (t *Term) Kill() {
	if t.signal(syscall.SIGHUP) {
		select {
		case <-t.done:
			return
		case <-time.After(killGrace):
		}
		t.signal(syscall.SIGKILL)
	}
	<-t.done
}

// signal sends sig to the program's process group, unless the program has
// been reaped (its PID could then belong to someone else).
func (t *Term) signal(sig syscall.Signal) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	pid := t.cmd.Process.Pid
	if t.reaped || pid <= 1 { // kill(-0) or kill(-1) would hit our own group or everyone
		return false
	}
	return syscall.Kill(-pid, sig) == nil
}

// read copies the program's output to out until the PTY reports EOF/EIO.
func (t *Term) read(out io.Writer) {
	defer close(t.readerDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			_, _ = out.Write(buf[:n]) // keep draining even if out fails: the program must never block on output
		}
		if err != nil {
			return
		}
	}
}

// write hands queued input to the program.
func (t *Term) write() {
	for {
		select {
		case <-t.wake:
		case <-t.done:
			return
		}
		t.mu.Lock()
		p := t.pending
		t.pending = nil
		t.mu.Unlock()
		if _, err := t.ptmx.Write(p); err != nil {
			return // the PTY is closed: the Term is finishing
		}
	}
}

// wait reaps the program, lets its last output drain, then finishes the Term.
func (t *Term) wait() {
	_ = t.cmd.Wait() // the exit status lands in cmd.ProcessState
	t.mu.Lock()
	t.reaped = true
	t.mu.Unlock()

	select {
	case <-t.readerDone:
	case <-time.After(drainTimeout): // a background job still holds the PTY open
	}

	t.mu.Lock()
	t.exited = true
	t.pending = nil
	t.mu.Unlock()
	// shortcut: on macOS the master is a blocking fd, so if an orphaned job
	// still holds the PTY, this close is deferred and the reader goroutine
	// stays blocked until that job exits; fine until it shows up in practice.
	_ = t.ptmx.Close()
	close(t.done)
}

func checkSize(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > 65535 || rows > 65535 {
		return fmt.Errorf("term: invalid size %dx%d", cols, rows)
	}
	return nil
}
