package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/R7rainz/gomux/internal/testutil"
)

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

func waitOutput(t *testing.T, out *testutil.Buffer, want string) {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, fmt.Sprintf("output containing %q", want), func() bool {
		return strings.Contains(out.String(), want)
	})
}

func signalOf(t *testing.T, tm *Term) syscall.Signal {
	t.Helper()
	ws := tm.ExitState().Sys().(syscall.WaitStatus)
	if !ws.Signaled() {
		t.Fatalf("not ended by a signal: %v", tm.ExitState())
	}
	return ws.Signal()
}

// groupGone waits until the process group has no members left.
func groupGone(t *testing.T, pgid int) {
	t.Helper()
	testutil.Eventually(t, 2*time.Second, fmt.Sprintf("process group %d to empty", pgid), func() bool {
		return syscall.Kill(-pgid, 0) != nil
	})
}

func TestOutputAndExitCode(t *testing.T) {
	var out testutil.Buffer
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
	var out testutil.Buffer
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
	var out testutil.Buffer
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
	var out testutil.Buffer
	tm := sh(t, `trap "" HUP; echo trapped; while :; do sleep 1; done`, &out)
	waitOutput(t, &out, "trapped") // the trap is installed before Kill runs
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
	var out testutil.Buffer
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
	var out testutil.Buffer
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

	// Into an idle queue, any size is accepted (a large paste).
	if _, err := tm.Write(make([]byte, 32)); err != nil {
		t.Fatalf("a write into an idle queue was refused: %v", err)
	}
	// Wait until the writer has delivered those bytes (the PTY buffers them).
	testutil.Eventually(t, 5*time.Second, "the writer to deliver the queued input", func() bool {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		return len(tm.pending) == 0 && tm.inflight == 0
	})
	// While bytes are in flight to the program, they count against the cap.
	tm.mu.Lock()
	tm.inflight = 10
	tm.mu.Unlock()
	if _, err := tm.Write(make([]byte, 7)); err == nil {
		t.Fatal("a write past the cap (counting in-flight bytes) was accepted")
	}
	if _, err := tm.Write(make([]byte, 6)); err != nil {
		t.Fatalf("a write within the cap failed: %v", err)
	}
}

// startHUPImmune starts a pane whose background subshell ignores SIGHUP and
// shares the pane's process group, and waits until that trap is installed.
//
// Readiness is a flag file the subshell creates after its trap, not printed
// output: a mainCmd that exits at once ends the pane within drainTimeout, and
// output after Done is discarded, so waiting for a printed marker raced and
// failed on a slow CI runner. mainCmd also waits for the flag before running.
func startHUPImmune(t *testing.T, mainCmd string) *Term {
	t.Helper()
	flag := filepath.Join(t.TempDir(), "immune")
	tm := sh(t, fmt.Sprintf(`(trap "" HUP; : > %[1]q; sleep 1000) &
while [ ! -e %[1]q ]; do sleep 0.01; done
%[2]s`, flag, mainCmd), io.Discard)
	t.Cleanup(func() { _ = syscall.Kill(-tm.Pid(), syscall.SIGKILL) }) // never leave the subshell behind
	testutil.Eventually(t, 5*time.Second, "the HUP-immune subshell to install its trap", func() bool {
		_, err := os.Stat(flag)
		return err == nil
	})
	return tm
}

// Regression: Kill used to stop once the main program died from SIGHUP,
// leaving HUP-ignoring members of its group running after pane kill and
// server shutdown.
func TestKillSweepsGroupAfterMainDies(t *testing.T) {
	old := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = old })
	tm := startHUPImmune(t, "sleep 100")
	tm.Kill()
	if sig := signalOf(t, tm); sig != syscall.SIGHUP {
		t.Fatalf("main program ended by %v, want SIGHUP", sig)
	}
	groupGone(t, tm.Pid()) // the HUP-immune subshell was SIGKILLed after the grace
}

// After Done the pane has ended: Kill signals nothing, as decided for jobs a
// pane leaves behind (CLAUDE.md §6.6), and because the group ID could by then
// belong to someone else.
func TestKillAfterDoneSignalsNothing(t *testing.T) {
	tm := startHUPImmune(t, "echo started")
	waitDone(t, tm, time.Second)
	tm.Kill()
	if err := syscall.Kill(-tm.Pid(), 0); err != nil {
		t.Fatalf("Kill after Done touched the leftover job: %v", err)
	}
}

// Output that an orphaned job writes after Done must not reach Output: the
// consumer (the pane's emulator) is torn down by then.
func TestNoOutputAfterDone(t *testing.T) {
	var out testutil.Buffer
	tm := sh(t, `(trap "" HUP; while :; do echo tick; sleep 0.05; done) & echo started`, &out)
	t.Cleanup(func() { _ = syscall.Kill(-tm.Pid(), syscall.SIGKILL) })
	waitDone(t, tm, time.Second)
	before := out.String()
	time.Sleep(300 * time.Millisecond) // observation window: the orphan keeps printing
	if after := out.String(); after != before {
		t.Fatalf("output arrived after Done: %q", strings.TrimPrefix(after, before))
	}
}
