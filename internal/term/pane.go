package term

import (
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/R7rainz/gomux/internal/proto"
)

// Pane is a program on a PTY plus the terminal emulator that keeps its screen
// (CLAUDE.md §6.3, §6.7). Its methods are safe for concurrent use.
type Pane struct {
	t   *Term
	mu  sync.Mutex // guards every emulator call except reads of its reply pipe, and bracketedPaste
	emu *vt.Emulator

	bracketedPaste bool // the program turned on bracketed paste (mode 2004)
}

// StartPane runs the program described by s with a new emulator of the same
// size as its output. s.Output must be nil: the emulator receives the output.
func StartPane(s Spec) (*Pane, error) {
	if s.Output != nil {
		return nil, errors.New("term: StartPane sets Spec.Output itself")
	}
	if err := checkSize(s.Cols, s.Rows); err != nil {
		return nil, err
	}
	p := &Pane{emu: vt.NewEmulator(s.Cols, s.Rows)}
	// Mode changes, including the ones a full reset (ESC c) makes, all go
	// through these callbacks, which run inside emulator calls, under p.mu.
	p.emu.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { p.bracketedPaste = p.bracketedPaste || m == ansi.ModeBracketedPaste },
		DisableMode: func(m ansi.Mode) { p.bracketedPaste = p.bracketedPaste && m != ansi.ModeBracketedPaste },
	})
	s.Output = screenWriter{p}
	t, err := Start(s)
	if err != nil {
		return nil, err
	}
	p.t = t
	go p.drainReplies()
	go func() {
		<-t.Done()
		// Ends drainReplies. Not emu.Close: in x/vt v0.1.0 it races with a
		// concurrent Read (CLAUDE.md §6.3).
		_ = p.emu.InputPipe().(io.Closer).Close()
	}()
	return p, nil
}

// screenWriter feeds the program's output to the pane's emulator.
type screenWriter struct{ p *Pane }

func (w screenWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.p.emu.Write(b)
}

// drainReplies hands the emulator's query replies and encoded input to the
// program. The emulator writes them into a synchronous pipe while p.mu is
// held, so this loop must never block: Term.Write queues without blocking,
// and once the queue is full (the program isn't reading its input) the bytes
// are dropped, so a stuck program stalls only its own input (CLAUDE.md §6.3).
func (p *Pane) drainReplies() {
	buf := make([]byte, 4096)
	for {
		n, err := p.emu.Read(buf)
		if n > 0 {
			_, _ = p.t.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// SendKey types a key, encoded for the modes the program has turned on
// (application cursor keys, for example). After Done it does nothing.
func (p *Pane) SendKey(k vt.KeyPressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emu.SendKey(k)
}

// Paste sends text as one paste, bracketed if the program turned that on.
// ESC and C1 controls are removed from text, so it can't end the bracket
// early and have the rest run as typed input (CLAUDE.md §13). Line breaks
// are sent as CR, which is what Enter sends, as real terminals and tmux do.
//
// A paste is all or nothing: it goes to the Term in a single Write, which
// takes it whole into an idle input queue, or refuses it whole with an error
// while the program isn't reading. (x/vt's own Paste writes the brackets and
// the text separately, in pipe-sized pieces, so a full queue could cut a
// paste or drop its closing bracket.) After Done it returns ErrExited.
func (p *Pane) Paste(text string) error {
	text = strings.Map(func(r rune) rune {
		if r == ansi.ESC || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, strings.NewReplacer("\r\n", "\r", "\n", "\r").Replace(text))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bracketedPaste {
		text = ansi.BracketedPasteStart + text + ansi.BracketedPasteEnd
	}
	// A barrier: the reply drain takes this empty pipe write only after it has
	// handed everything written before it to the Term, so the paste can't
	// overtake keys still on their way.
	p.emu.SendText("")
	_, err := p.t.Write([]byte(text))
	return err
}

// Resize sets the new size on the PTY, then on the emulator. Both happen
// under p.mu, which the program's output also needs, so its redraw after
// SIGWINCH reaches an emulator that already has the new size. If the PTY
// refuses (the program has exited), nothing changes.
func (p *Pane) Resize(cols, rows int) error {
	if err := checkSize(cols, rows); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.t.Resize(cols, rows); err != nil {
		return err
	}
	p.emu.Resize(cols, rows)
	return nil
}

// Snapshot returns the visible screen, all from one moment. After Done it
// returns the last screen.
func (p *Pane) Snapshot() proto.Screen {
	p.mu.Lock()
	defer p.mu.Unlock()
	return snapshot(p.emu)
}

// Kill stops the program; see Term.Kill.
func (p *Pane) Kill() { p.t.Kill() }

// Done is closed once the program has ended; see Term.Done.
func (p *Pane) Done() <-chan struct{} { return p.t.Done() }

// ExitState reports how the program ended. Valid after Done is closed.
func (p *Pane) ExitState() *os.ProcessState { return p.t.ExitState() }

// Pid is the program's process ID and process-group ID.
func (p *Pane) Pid() int { return p.t.Pid() }

func snapshot(e *vt.Emulator) proto.Screen {
	cols, rows := e.Width(), e.Height()
	s := proto.Screen{Cols: cols, Rows: rows, Lines: make([]proto.Line, rows)}
	for y := range rows {
		s.Lines[y] = lineAt(e, y, cols)
	}
	pos := e.CursorPosition()
	shape, blink := e.CursorStyle()
	s.Cursor = proto.Cursor{X: pos.X, Y: pos.Y, Hidden: e.CursorHidden(), Shape: cursorShapes[shape], Blink: blink}
	return s
}

// lineAt converts row y into runs of equally styled text, without the
// default-styled blanks at its end.
func lineAt(e *vt.Emulator, y, cols int) proto.Line {
	l := proto.Line{}
	var text strings.Builder
	var st proto.Style
	flush := func() {
		if text.Len() > 0 {
			l = append(l, proto.Run{Text: text.String(), Style: st})
			text.Reset()
		}
	}
	for x := range cols {
		c := e.CellAt(x, y)
		if c == nil {
			c = &uv.EmptyCell
		}
		if c.Width == 0 {
			continue // the second column of a wide character
		}
		// Compare converted styles: x/vt's Style.Equal matches colors by RGB, so
		// palette 9, palette 196 and #ff0000 would merge into one run.
		if s := styleOf(&c.Style); s != st {
			flush()
			st = s
		}
		if c.Content == "" {
			text.WriteByte(' ')
		} else {
			text.WriteString(c.Content)
		}
	}
	flush()
	if n := len(l); n > 0 && l[n-1].Style == (proto.Style{}) {
		if t := strings.TrimRight(l[n-1].Text, " "); t == "" {
			l = l[:n-1]
		} else {
			l[n-1].Text = t
		}
	}
	return l
}

func styleOf(s *uv.Style) proto.Style {
	var attrs proto.Attr
	for _, m := range attrMap {
		if s.Attrs&m.from != 0 {
			attrs |= m.to
		}
	}
	return proto.Style{
		Fg:             colorOf(s.Fg),
		Bg:             colorOf(s.Bg),
		UnderlineColor: colorOf(s.UnderlineColor),
		Attrs:          attrs,
		Underline:      underlines[s.Underline],
	}
}

// colorOf keeps palette colors as palette indexes, so the user's terminal
// theme applies to them, and sends only true colors as RGB.
func colorOf(c color.Color) proto.Color {
	switch c := c.(type) {
	case nil:
		return ""
	case ansi.BasicColor:
		return proto.Color(strconv.Itoa(int(c)))
	case ansi.IndexedColor:
		return proto.Color(strconv.Itoa(int(c)))
	default:
		r, g, b, _ := c.RGBA()
		return proto.Color(fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8))
	}
}

// The mappings from x/vt's values to the protocol's, which must not move
// when the library changes.
var (
	attrMap = []struct {
		from uint8
		to   proto.Attr
	}{
		{uv.AttrBold, proto.AttrBold},
		{uv.AttrFaint, proto.AttrFaint},
		{uv.AttrItalic, proto.AttrItalic},
		{uv.AttrBlink | uv.AttrRapidBlink, proto.AttrBlink},
		{uv.AttrReverse, proto.AttrReverse},
		{uv.AttrConceal, proto.AttrConceal},
		{uv.AttrStrikethrough, proto.AttrStrikethrough},
	}
	underlines = map[uv.Underline]proto.Underline{
		uv.UnderlineSingle: proto.UnderlineSingle,
		uv.UnderlineDouble: proto.UnderlineDouble,
		uv.UnderlineCurly:  proto.UnderlineCurly,
		uv.UnderlineDotted: proto.UnderlineDotted,
		uv.UnderlineDashed: proto.UnderlineDashed,
	}
	cursorShapes = map[vt.CursorStyle]proto.CursorShape{
		vt.CursorBlock:     proto.CursorBlock,
		vt.CursorUnderline: proto.CursorUnderline,
		vt.CursorBar:       proto.CursorBar,
	}
)
