package proto

// Screen is what one pane shows: its visible cells as styled text, and the
// cursor (CLAUDE.md §6.7).
type Screen struct {
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
	Lines  []Line `json:"lines"` // Rows lines, top to bottom
	Cursor Cursor `json:"cursor"`
}

// Line is one screen row as runs of equally styled text. Cells after the last
// run are blank, in the default style. A wide character (CJK, most emoji)
// appears once in Text and takes two columns.
type Line []Run

// Run is consecutive cells sharing one style.
type Run struct {
	Text  string `json:"text"`
	Style Style  `json:"style,omitzero"`
}

// Style is how a run is drawn. The zero Style is the terminal's default.
type Style struct {
	Fg             Color     `json:"fg,omitempty"`
	Bg             Color     `json:"bg,omitempty"`
	UnderlineColor Color     `json:"ul_color,omitempty"`
	Attrs          Attr      `json:"attrs,omitempty"`
	Underline      Underline `json:"underline,omitempty"`
}

// Color is "" for the terminal's default color, "0" to "255" for an entry of
// the terminal's palette (so the user's theme applies), or "#rrggbb".
type Color string

// Attr is a set of text attributes.
type Attr uint8

// Text attributes. The values are part of the protocol: never renumber them.
const (
	AttrBold Attr = 1 << iota
	AttrFaint
	AttrItalic
	AttrBlink
	AttrReverse
	AttrConceal
	AttrStrikethrough
)

// Underline is the underline style.
type Underline uint8

// Underline styles. The values are part of the protocol: never renumber them.
const (
	UnderlineNone Underline = iota
	UnderlineSingle
	UnderlineDouble
	UnderlineCurly
	UnderlineDotted
	UnderlineDashed
)

// Cursor is the cursor's cell, visibility and look.
type Cursor struct {
	X      int         `json:"x"`
	Y      int         `json:"y"`
	Hidden bool        `json:"hidden,omitempty"`
	Shape  CursorShape `json:"shape,omitempty"`
	Blink  bool        `json:"blink,omitempty"`
}

// CursorShape is the cursor's shape.
type CursorShape uint8

// Cursor shapes. The values are part of the protocol: never renumber them.
const (
	CursorBlock CursorShape = iota
	CursorUnderline
	CursorBar
)
