package proto

import (
	"encoding/json"
	"testing"
)

// The JSON shape is the wire format: names, omitted defaults and constant
// values must not change within a protocol version.
func TestScreenWireFormat(t *testing.T) {
	s := Screen{
		Cols: 4, Rows: 2,
		Lines: []Line{
			{{Text: "a"}, {Text: "日", Style: Style{Fg: "#ff6400", Bg: "4", UnderlineColor: "200", Attrs: AttrBold | AttrStrikethrough, Underline: UnderlineCurly}}},
			{},
		},
		Cursor: Cursor{X: 3, Y: 1, Hidden: true, Shape: CursorBar, Blink: true},
	}
	got, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cols":4,"rows":2,"lines":[[{"text":"a"},{"text":"日","style":{"fg":"#ff6400","bg":"4","ul_color":"200","attrs":65,"underline":3}}],[]],"cursor":{"x":3,"y":1,"hidden":true,"shape":2,"blink":true}}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
