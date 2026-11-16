package term

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// output is a goroutine-safe sink for Spec.Output.
type output struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) { o.mu.Lock(); defer o.mu.Unlock(); return o.b.Write(p) }
func (o *output) String() string              { o.mu.Lock(); defer o.mu.Unlock(); return o.b.String() }

// sh runs script with /bin/sh on a 80x24 PTY. Scripts keep the tests
// deterministic on Linux and macOS, whatever the user's shell is.
func sh(t *testing.T, script string, out io.Writer) *Term {
	t.Helper()
	tm, err := Start(Spec{Path: "/bin/sh", Args: []string{"sh", "-c", script}, Cols: 80, Rows: 24, Output: out})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tm.Kill)
	return tm
}

func waitDone(t *testing.T, tm *Term, within time.Duration) {
	t.Helper()
	select {
	case <-tm.Done():
	case <-time.After(within):
		t.Fatalf("not done within %v", within)
	}
}

func waitOutput(t *testing.T, out *output, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), want); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("output never contained %q; got %q", want, out.String())
		}
	}
}

func signalOf(t *testing.T, tm *Term) syscall.Signal {
	t.Helper()
	ws := tm.ExitState().Sys().(syscall.WaitStatus)
	if !ws.Signaled() {
		t.Fatalf("not ended by a signal: %v", tm.ExitState())
	}
	return ws.Signal()
}

// groupGone polls until the process group has no members left.
func groupGone(t *testing.T, pgid int) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); syscall.Kill(-pgid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still has members", pgid)
		}
	}
}

func TestOutputAndExitCode(t *testing.T) {
	var out output
	tm := sh(t, "printf hello; exit 3", &out)
	waitDone(t, tm, 5*time.Second)
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("output %q", out.String())
	}
	if code := tm.ExitState().ExitCode(); code != 3 {
		t.Fatalf("exit code %d, want 3", code)
	}
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(tm.Pid(), &ws, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("process not reaped (zombie?): %v", err)
	}
}

func TestInput(t *testing.T) {
	var out output
	tm := sh(t, `read x; printf "got-%s" "$x"`, &out)
	if _, err := tm.Write([]byte("abc\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, &out, "got-abc")
	waitDone(t, tm, 5*time.Second)
	if _, err := tm.Write([]byte("late")); !errors.Is(err, ErrExited) {
		t.Fatalf("write after exit: %v", err)
	}
	if err := tm.Resize(10, 10); !errors.Is(err, ErrExited) {
		t.Fatalf("resize after exit: %v", err)
	}
}

func TestResize(t *testing.T) {
	var out output
	tm := sh(t, "stty size; read _; stty size", &out)
	waitOutput(t, &out, "24 80")
	if err := tm.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, &out, "30 100")
}

func TestKillHangsUp(t *testing.T) {
	tm, err := Start(Spec{Path: "sleep", Args: []string{"sleep", "100"}, Cols: 80, Rows: 24, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	tm.Kill()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Kill took %v; SIGHUP should end sleep at once", d)
	}
	if sig := signalOf(t, tm); sig != syscall.SIGHUP {
		t.Fatalf("ended by %v, want SIGHUP", sig)
	}
}

func TestKillForcesStubbornGroup(t *testing.T) {
	old := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = old })
	// The ignored SIGHUP is inherited by the sleep children: the whole group resists.
	tm := sh(t, `trap "" HUP; while :; do sleep 1; done`, io.Discard)
	time.Sleep(100 * time.Millisecond) // let the trap take effect
	start := time.Now()
	tm.Kill()
	if d := time.Since(start); d < killGrace {
		t.Fatalf("Kill returned after %v, before the grace period", d)
	}
	if sig := signalOf(t, tm); sig != syscall.SIGKILL {
		t.Fatalf("ended by %v, want SIGKILL", sig)
	}
	groupGone(t, tm.Pid())
}

func TestDoneDoesNotWaitForOrphans(t *testing.T) {
	var out output
	// The subshell ignores SIGHUP and keeps the PTY open after sh exits.
	tm := sh(t, `(trap "" HUP; sleep 3) & echo started`, &out)
	t.Cleanup(func() { _ = syscall.Kill(-tm.Pid(), syscall.SIGKILL) }) // the orphan shares sh's group
	waitDone(t, tm, time.Second)
	if !strings.Contains(out.String(), "started") {
		t.Fatalf("output %q", out.String())
	}
}

func TestNoDescriptorLeaks(t *testing.T) {
	first := sh(t, "sleep 100", io.Discard) // its PTY master must not leak into the second
	_ = first
	// The shell's builtin test checks its own descriptors; an external program
	// such as ls would add descriptors of its own.
	var out output
	// First prove the probe sees an fd we open on purpose, so the check can't pass vacuously.
	tm := sh(t, `exec 9</dev/null; [ -e /dev/fd/9 ] && echo probe-works; exec 9<&-
for f in 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do [ -e /dev/fd/$f ] && echo "leaked-$f"; done; echo checked`, &out)
	waitDone(t, tm, 5*time.Second)
	if s := out.String(); !strings.Contains(s, "probe-works") || !strings.Contains(s, "checked") || strings.Contains(s, "leaked-") {
		t.Fatalf("the child holds descriptors beyond 0-2, or the probe is broken: %q", s)
	}
}

func TestStartErrors(t *testing.T) {
	cases := map[string]Spec{
		"missing executable": {Path: "/nonexistent/gomux-test", Cols: 80, Rows: 24, Output: io.Discard},
		"missing dir":        {Path: "/bin/sh", Dir: "/nonexistent/dir", Cols: 80, Rows: 24, Output: io.Discard},
		"zero size":          {Path: "/bin/sh", Cols: 0, Rows: 24, Output: io.Discard},
		"too large":          {Path: "/bin/sh", Cols: 80, Rows: 70000, Output: io.Discard},
		"no output":          {Path: "/bin/sh", Cols: 80, Rows: 24},
	}
	for name, s := range cases {
		if tm, err := Start(s); err == nil {
			tm.Kill()
			t.Errorf("%s: Start succeeded", name)
		}
	}
}

func TestKillIsIdempotent(t *testing.T) {
	tm := sh(t, "sleep 100", io.Discard)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(tm.Kill)
	}
	wg.Wait()
	tm.Kill() // after Done: returns at once, signals nothing
}

func TestInputCap(t *testing.T) {
	old := maxPending
	maxPending = 16
	t.Cleanup(func() { maxPending = old })
	tm := sh(t, "sleep 100", io.Discard)
	if _, err := tm.Write(make([]byte, 17)); err == nil {
		t.Fatal("a write over the cap was accepted")
	}
	if _, err := tm.Write([]byte("fits\n")); err != nil {
		t.Fatalf("a small write failed: %v", err)
	}
}
