package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/R7rainz/gomux/internal/proto"
	"github.com/R7rainz/gomux/internal/testutil"
)

// shPane runs script with /bin/sh in a pane of 80x24.
func shPane(t *testing.T, script string) *Pane {
	t.Helper()
	p, err := StartPane(Spec{Path: "/bin/sh", Args: []string{"sh", "-c", script}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Kill)
	return p
}

// screenText is the screen as plain text, one line per row.
func screenText(s proto.Screen) string {
	var b strings.Builder
	for _, l := range s.Lines {
		for _, r := range l {
			b.WriteString(r.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func waitScreen(t *testing.T, p *Pane, want string) {
	t.Helper()
	testutil.Eventually(t, 5*time.Second, "the screen to show "+want, func() bool {
		return strings.Contains(screenText(p.Snapshot()), want)
	})
}

// within fails the test if f doesn't return within d.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	returned := make(chan struct{})
	go func() { f(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(d):
		t.Fatalf("%s didn't return within %v", what, d)
	}
}

func key(code rune) vt.KeyPressEvent { return vt.KeyPressEvent{Code: code} }

func TestSnapshotTurnsCellsIntoStyledRuns(t *testing.T) {
	cases := []struct {
		name, output string
		want         proto.Line
	}{
		{"plain text without trailing blanks", "hi", proto.Line{{Text: "hi"}}},
		{"empty line", "", proto.Line{}},
		{"blanks inside a line are kept", "a\x1b[3Cb", proto.Line{{Text: "a   b"}}},
		{"truecolor", "\x1b[38;2;255;100;0mX", proto.Line{{Text: "X", Style: proto.Style{Fg: "#ff6400"}}}},
		{"palette colors stay indexes", "\x1b[31mA\x1b[38;5;200;44mB", proto.Line{
			{Text: "A", Style: proto.Style{Fg: "1"}},
			{Text: "B", Style: proto.Style{Fg: "200", Bg: "4"}},
		}},
		{"same RGB in different kinds of color stays apart", "\x1b[91mA\x1b[38;5;196mB\x1b[38;2;255;0;0mC\x1b[30mD\x1b[38;5;16mE", proto.Line{
			{Text: "A", Style: proto.Style{Fg: "9"}},
			{Text: "B", Style: proto.Style{Fg: "196"}},
			{Text: "C", Style: proto.Style{Fg: "#ff0000"}},
			{Text: "D", Style: proto.Style{Fg: "0"}},
			{Text: "E", Style: proto.Style{Fg: "16"}},
		}},
		{"attributes", "\x1b[1;3;7mX", proto.Line{{Text: "X", Style: proto.Style{Attrs: proto.AttrBold | proto.AttrItalic | proto.AttrReverse}}}},
		{"curly underline with a color", "\x1b[4:3;58;5;9mX", proto.Line{{Text: "X", Style: proto.Style{Underline: proto.UnderlineCurly, UnderlineColor: "9"}}}},
		{"wide characters once each", "日本👍🏽.", proto.Line{{Text: "日本👍🏽."}}},
		{"same style merges, a change splits", "ab\x1b[1mcd\x1b[0mef", proto.Line{
			{Text: "ab"},
			{Text: "cd", Style: proto.Style{Attrs: proto.AttrBold}},
			{Text: "ef"},
		}},
		{"colored blanks are kept", "\x1b[41m  ", proto.Line{{Text: "  ", Style: proto.Style{Bg: "1"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := vt.NewEmulator(20, 2)
			_, _ = e.WriteString(c.output)
			s := snapshot(e)
			if s.Cols != 20 || s.Rows != 2 || len(s.Lines) != 2 {
				t.Fatalf("size %dx%d with %d lines, want 20x2 with 2", s.Cols, s.Rows, len(s.Lines))
			}
			if !reflect.DeepEqual(s.Lines[0], c.want) {
				t.Fatalf("got  %#v\nwant %#v", s.Lines[0], c.want)
			}
		})
	}
}

// Each step's sequence is written after the previous ones, on one emulator.
func TestSnapshotReportsTheCursor(t *testing.T) {
	e := vt.NewEmulator(20, 5)
	steps := []struct {
		name, output string
		want         proto.Cursor
	}{
		{"moved, default look", "\x1b[2;4H", proto.Cursor{X: 3, Y: 1, Shape: proto.CursorBlock, Blink: true}},
		{"blinking bar", "\x1b[5 q", proto.Cursor{X: 3, Y: 1, Shape: proto.CursorBar, Blink: true}},
		{"hidden steady underline", "\x1b[?25l\x1b[4 q", proto.Cursor{X: 3, Y: 1, Hidden: true, Shape: proto.CursorUnderline}},
	}
	for _, st := range steps {
		_, _ = e.WriteString(st.output)
		if c := snapshot(e).Cursor; c != st.want {
			t.Fatalf("%s: got %+v, want %+v", st.name, c, st.want)
		}
	}
}

func TestPaneShowsOutput(t *testing.T) {
	p := shPane(t, `printf 'hello \033[1mworld\033[0m'; sleep 100`)
	waitScreen(t, p, "hello world")
	if got := p.Snapshot().Lines[0]; !reflect.DeepEqual(got, proto.Line{{Text: "hello "}, {Text: "world", Style: proto.Style{Attrs: proto.AttrBold}}}) {
		t.Fatalf("first line %#v", got)
	}
}

// The program asks for its title back (CSI 21 t) and for the clipboard (OSC
// 52 ; ?), then for the device attributes (DA1), and reads its input. Only
// the DA1 answer may arrive: the other two would let output type
// attacker-chosen text into the shell (CLAUDE.md §13).
func TestPaneAnswersQueriesButNotHostileOnes(t *testing.T) {
	p := shPane(t, `stty -echo
printf '\033]0;evil-title\007\033[21t\033]52;c;?\007\033[c'
echo ready
read -r reply
printf 'got:%s\n' "$reply" | tr '\033' E
sleep 100`)
	waitScreen(t, p, "ready") // the queries were parsed, so any replies are queued
	p.SendKey(key(vt.KeyEnter))
	waitScreen(t, p, "got:")
	s := screenText(p.Snapshot())
	if !strings.Contains(s, "got:E[?") || strings.Contains(s, "evil") || strings.Contains(s, "52;") {
		t.Fatalf("the input should hold only the DA1 answer:\n%s", s)
	}
}

func TestPaneKeysFollowProgramModes(t *testing.T) {
	// Application cursor keys and bracketed paste on, then dump 16 input bytes.
	p := shPane(t, `stty -icanon -echo
printf '\033[?1h\033[?2004h'; echo ready
dd bs=1 count=16 2>/dev/null | od -An -tx1 | tr -d ' \n'; echo
sleep 100`)
	waitScreen(t, p, "ready")
	p.SendKey(key(vt.KeyUp))
	if err := p.Paste("x"); err != nil {
		t.Fatal(err)
	}
	// ESC O A, then ESC [200~ x ESC [201~
	waitScreen(t, p, "1b4f41"+"1b5b3230307e"+"78"+"1b5b3230317e")
}

// Pasted text can't end the bracketed paste early: if "\x1b[201~" in it got
// through, the shell would run whatever follows as typed input (CLAUDE.md §13).
func TestPasteCannotEndTheBracketEarly(t *testing.T) {
	p := shPane(t, `stty -icanon -echo
printf '\033[?2004h'; echo ready
dd bs=1 count=20 2>/dev/null | od -An -tx1 | tr -d ' \n'; echo
sleep 100`)
	waitScreen(t, p, "ready")
	if err := p.Paste("a\x1b[201~b\u009bc"); err != nil { // ESC and the C1 CSI are removed
		t.Fatal(err)
	}
	// ESC [200~, then "a[201~bc", then ESC [201~
	waitScreen(t, p, "1b5b3230307e"+"615b3230317e6263"+"1b5b3230317e")
}

// Keys typed before and after a paste reach the program in that order, even
// though the paste skips the emulator's pipe.
func TestPasteKeepsOrderWithKeys(t *testing.T) {
	const rounds = 20 // a reordering race would show up in some round
	p := shPane(t, fmt.Sprintf(`stty -icanon -echo; echo ready; dd bs=1 count=%d 2>/dev/null; echo; sleep 100`, 3*rounds))
	waitScreen(t, p, "ready")
	for range rounds {
		p.SendKey(vt.KeyPressEvent{Code: 'a', Text: "a"})
		if err := p.Paste("b"); err != nil {
			t.Fatal(err)
		}
		p.SendKey(vt.KeyPressEvent{Code: 'c', Text: "c"})
	}
	waitScreen(t, p, strings.Repeat("abc", rounds))
}

// Regression: a paste larger than the input cap, sent while the program isn't
// reading yet, arrives whole, closing bracket included. x/vt's own Paste went
// through the pipe in 4 KiB pieces, and the pieces beyond the cap were dropped.
func TestLargePasteArrivesWhole(t *testing.T) {
	old := maxPending
	maxPending = 64 << 10
	t.Cleanup(func() { maxPending = old })
	flag := filepath.Join(t.TempDir(), "read-now")
	const size = 200 << 10
	p := shPane(t, fmt.Sprintf(`stty -icanon -echo
printf '\033[?2004h'; echo ready
while [ ! -e %q ]; do sleep 0.01; done
head -c %d | tail -c 6 | od -An -tx1 | tr -d ' \n'; echo
sleep 100`, flag, size+12))
	waitScreen(t, p, "ready")
	if err := p.Paste(strings.Repeat("x", size)); err != nil {
		t.Fatalf("a paste into an idle queue was refused: %v", err)
	}
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitScreen(t, p, "1b5b3230317e") // all of it arrived, ending with ESC [201~
}

// While the program isn't reading and the queue is busy, a paste that doesn't
// fit is refused whole, with an error, rather than cut.
func TestPasteRefusedWholeWhenQueueFull(t *testing.T) {
	old := maxPending
	maxPending = 64
	t.Cleanup(func() { maxPending = old })
	p := shPane(t, `stty -icanon -echo; echo ready; sleep 100`) // never reads
	waitScreen(t, p, "ready")
	if err := p.Paste(strings.Repeat("x", 64<<10)); err != nil { // idle queue: taken whole
		t.Fatal(err)
	}
	testutil.Eventually(t, 5*time.Second, "the writer to block on the full terminal", func() bool {
		p.t.mu.Lock()
		defer p.t.mu.Unlock()
		return p.t.inflight > 0
	})
	if err := p.Paste("more"); err == nil {
		t.Fatal("a paste into a full queue was accepted")
	}
}

func TestResizeReachesProgramAndScreen(t *testing.T) {
	p := shPane(t, `stty size; read _; stty size; sleep 100`)
	waitScreen(t, p, "24 80")
	if err := p.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if s := p.Snapshot(); s.Cols != 100 || s.Rows != 30 || len(s.Lines) != 30 {
		t.Fatalf("snapshot is %dx%d with %d lines after resize", s.Cols, s.Rows, len(s.Lines))
	}
	p.SendKey(key(vt.KeyEnter))
	waitScreen(t, p, "30 100")
	if err := p.Resize(0, 10); err == nil {
		t.Fatal("an invalid size was accepted")
	}
	if s := p.Snapshot(); s.Cols != 100 || s.Rows != 30 {
		t.Fatalf("a refused resize changed the screen to %dx%d", s.Cols, s.Rows)
	}
}

// Regression (CLAUDE.md §6.3): the emulator writes its replies while holding
// the pane's lock. A program that floods queries and never reads its input
// must not be able to block that lock, and with it every snapshot.
func TestStuckProgramDoesNotStallScreen(t *testing.T) {
	old := maxPending
	maxPending = 64
	t.Cleanup(func() { maxPending = old })
	p := shPane(t, `stty -icanon -echo; echo flooding; while :; do printf '\033[6n'; done`)
	waitScreen(t, p, "flooding")
	// Full means queued plus in-flight bytes leave no room for another reply
	// (at most 8 bytes here). Under load the writer takes larger batches, so
	// pending alone can stay small while the queue is full.
	testutil.Eventually(t, 10*time.Second, "the program's input queue to fill up", func() bool {
		p.t.mu.Lock()
		defer p.t.mu.Unlock()
		return len(p.t.pending)+p.t.inflight > maxPending-8
	})
	for range 20 { // replies keep being dropped while the program floods
		within(t, 2*time.Second, "Snapshot", func() { p.Snapshot() })
		within(t, 2*time.Second, "SendKey", func() { p.SendKey(key('x')) })
	}
	within(t, 5*time.Second, "Kill", p.Kill)
}

func TestPaneKeepsLastScreenAfterExit(t *testing.T) {
	p := shPane(t, `printf bye`)
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not done")
	}
	if !strings.Contains(screenText(p.Snapshot()), "bye") {
		t.Fatal("the last screen is gone after exit")
	}
	within(t, 2*time.Second, "SendKey and Paste after exit", func() {
		p.SendKey(key(vt.KeyEnter))
		if err := p.Paste("late"); !errors.Is(err, ErrExited) {
			t.Errorf("paste after exit: %v", err)
		}
	})
	if err := p.Resize(10, 10); !errors.Is(err, ErrExited) {
		t.Fatalf("resize after exit: %v", err)
	}
	if s := p.Snapshot(); s.Cols != 80 || s.Rows != 24 {
		t.Fatalf("a resize refused after exit changed the screen to %dx%d", s.Cols, s.Rows)
	}
	// The reply pipe is closed, so its drain goroutine has ended.
	testutil.Eventually(t, 2*time.Second, "the reply pipe to close", func() bool {
		_, err := p.emu.InputPipe().Write(nil)
		return errors.Is(err, io.ErrClosedPipe)
	})
}

func TestStartPaneRefusesBadSpecs(t *testing.T) {
	cases := map[string]Spec{
		"output set":         {Path: "/bin/sh", Cols: 80, Rows: 24, Output: io.Discard},
		"missing executable": {Path: "/nonexistent/gomux-test", Cols: 80, Rows: 24},
		"zero size":          {Path: "/bin/sh", Cols: 0, Rows: 24},
	}
	for name, s := range cases {
		if p, err := StartPane(s); err == nil {
			p.Kill()
			t.Errorf("%s: StartPane succeeded", name)
		}
	}
}
