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
	// killGrace is how long Kill waits after SIGHUP before sending SIGKILL,
	// and how long it then waits for the program to finish.
	killGrace = 2 * time.Second
	// drainTimeout is how long the program's last output may take to arrive
	// after it exits, before the Term counts as done.
	drainTimeout = 200 * time.Millisecond
	// maxPending caps input queued or in flight to the program.
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
	// Output receives everything the program writes, from one goroutine,
	// until Done is closed; output arriving after that is discarded.
	Output io.Writer
}

// Term is one program running on a PTY.
type Term struct {
	cmd        *exec.Cmd
	ptmx       *os.File // the PTY master
	done       chan struct{}
	readerDone chan struct{}
	wake       chan struct{} // capacity 1: input is pending

	mu       sync.Mutex
	pending  []byte // input not yet taken by the writer goroutine
	inflight int    // bytes the writer is currently writing
	exited   bool   // the program has exited or its PTY broke; Write and Resize fail
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

// Write queues p as input to the program and never blocks. While input is
// already queued or in flight, it fails if accepting p would exceed
// maxPending, so a program that stops reading its input can only stall
// itself. A write into an idle queue is accepted whatever its size (a paste).
func (t *Term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exited {
		return 0, ErrExited
	}
	if queued := len(t.pending) + t.inflight; queued > 0 && queued+len(p) > maxPending {
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

// Kill stops everything in the program's process group: SIGHUP first (what
// closing a terminal window sends), then SIGKILL for any member still alive
// after killGrace, even if the main program has died meanwhile. It returns
// once Done is closed, or after a further killGrace if the program can't be
// signalled (e.g. it runs as root via sudo). It is safe to call repeatedly
// and concurrently.
//
// Once Done is closed, Kill does nothing: the pane has ended, jobs it left
// behind are treated like a closed window's (CLAUDE.md §6.6), and its group
// ID may by then belong to an unrelated group after PID wraparound.
func (t *Term) Kill() {
	select {
	case <-t.done:
		return
	default:
	}
	if t.signalGroup(syscall.SIGHUP) {
		for deadline := time.Now().Add(killGrace); t.groupAlive() && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
		if t.groupAlive() {
			t.signalGroup(syscall.SIGKILL)
		}
	}
	select {
	case <-t.done:
	case <-time.After(killGrace):
	}
}

// signalGroup sends sig to the program's process group. It is used only while
// the pane is live or its group still has members: a process-group ID can't
// be reused while any member is alive, so the signal reaches only this pane's
// processes, and an empty group yields ESRCH.
func (t *Term) signalGroup(sig syscall.Signal) bool {
	pgid := t.cmd.Process.Pid
	if pgid <= 1 { // kill(-0) or kill(-1) would hit our own group or everyone
		return false
	}
	return syscall.Kill(-pgid, sig) == nil
}

// groupAlive reports whether the process group still has members, including
// ones this user may not signal (EPERM, e.g. a sudo child).
func (t *Term) groupAlive() bool {
	err := syscall.Kill(-t.cmd.Process.Pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// read copies the program's output to out until the PTY reports EOF/EIO. It
// keeps reading after Done, so an orphaned job holding the PTY never blocks,
// but discards that output.
func (t *Term) read(out io.Writer) {
	defer close(t.readerDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			select {
			case <-t.done:
			default:
				_, _ = out.Write(buf[:n]) // keep draining even if out fails: the program must never block on output
			}
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
		t.pending, t.inflight = nil, len(p)
		t.mu.Unlock()
		_, err := t.ptmx.Write(p)
		t.mu.Lock()
		t.inflight = 0
		if err != nil {
			t.exited = true // the PTY is gone: later writes would never be delivered
		}
		t.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// wait reaps the program, lets its last output drain, then finishes the Term.
func (t *Term) wait() {
	_ = t.cmd.Wait() // the exit status lands in cmd.ProcessState

	select {
	case <-t.readerDone:
	case <-time.After(drainTimeout): // a background job still holds the PTY open
	}

	t.mu.Lock()
	t.exited = true
	t.pending = nil
	t.mu.Unlock()
	// shortcut: on macOS the master is a blocking fd, so if an orphaned job
	// still holds the PTY, this close is deferred and the reader (and a writer
	// blocked on input the orphan never reads) stay parked until that job
	// exits or Kill sweeps the group; fine until it shows up in practice.
	_ = t.ptmx.Close()
	close(t.done)
}

func checkSize(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > 65535 || rows > 65535 {
		return fmt.Errorf("term: invalid size %dx%d", cols, rows)
	}
	return nil
}
