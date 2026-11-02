package textsel

import (
	"testing"

	"charm.land/bubbles/v2/viewport"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestUnframedTakesTheBubbleOffAMessage(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{
			in: "   ╭──────────────╮",
		},
		{
			in: "   ╰──────────────╯",
		},
		{
			in:   "   │ give me a test │",
			want: "give me a test",
			ok:   true,
		},
		{
			in:   "ive me a test │",
			want: "ive me a test",
			ok:   true,
		},
		{
			in:   "   │ give me",
			want: "give me",
			ok:   true,
		},
		{
			in:   "    indented code",
			want: "    indented code",
			ok:   true,
		},
		{
			in: "",
			ok: true,
		},
		{
			in:   "────",
			want: "────",
			ok:   true,
		},
	}
	for _, tt := range tests {
		got, ok := unframed(tt.in)
		assert.Check(t, cmp.Equal(got, tt.want), "unframed(%q)", tt.in)
		assert.Check(t, cmp.Equal(ok, tt.ok), "unframed(%q)", tt.in)
	}
}

func TestADragOverAPaneCopiesItsTextWithoutTheMarginOrFrame(t *testing.T) {
	v := viewport.New()
	v.SetWidth(30)
	v.SetHeight(5)
	p := Pane{View: &v, X: 2, Y: 1, Margin: 1, Framed: true}
	var d Drag
	d.SetContent(p, " ╭──────╮\n │ hi   │\n ╰──────╯\n reply text")

	above := d.Press(p, 1, 0)
	assert.Check(t, !above, "a press above the pane began a selection")
	began := d.Press(p, 2, 1)
	assert.Check(t, began)
	d.Move(p, 12, 4)

	got := d.Release(p)
	assert.Check(t, cmp.Equal(got, "hi\nreply text"))
	assert.Check(t, !d.Held())
}
