// Package testutil holds helpers shared by gomux's tests (CLAUDE.md
// "Testing"). Only _test.go files may import it;
// TestNotLinkedIntoGomux checks it never reaches the gomux binary.
package testutil

import (
	"bytes"
	"os"
	"sync"
	"testing"
	"time"
)

// SocketDir returns a short private directory under /tmp for Unix sockets,
// removed when the test ends. macOS limits socket paths to 104 bytes, and
// t.TempDir() paths are longer than that.
func SocketDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gmx") // created with mode 0700
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// Eventually polls cond until it reports true, and fails the test with what
// if that doesn't happen within timeout. Use it instead of sleeping to wait
// for something asynchronous. Call it from the test's own goroutine.
func Eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
	}
}

// Buffer is a bytes.Buffer that one goroutine can write while another reads,
// e.g. a program's output captured while the test inspects it.
type Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
