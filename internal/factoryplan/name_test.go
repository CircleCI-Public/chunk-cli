package factoryplan

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// TestFallbackTitleSlugsFirstLine checks the fallback title is a short slug
// of the first line, cut at a whole word, and counted in runes so a
// multi-byte word is not cut mid-character.
func TestFallbackTitleSlugsFirstLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{
			name: "first line only",
			text: "Add a --dry-run flag\nmore detail",
			want: "add-a-dry-run-flag",
		},
		{
			name: "punctuation and case",
			text: "  Fix `chunk watch`'s crash (on resize)!  ",
			want: "fix-chunk-watch-s-crash-on-resize",
		},
		{
			name: "stops at a whole word",
			text: "make the plan name a short slug instead of the whole first line",
			want: "make-the-plan-name-a-short-slug-instead",
		},
		{
			name: "cuts one long word",
			text: strings.Repeat("é", 50) + " tail",
			want: strings.Repeat("é", 40),
		},
		{
			name: "no words",
			text: "?!\nsecond line",
			want: "plan",
		},
		{
			name: "empty",
			text: "",
			want: "plan",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(fallbackTitle(tc.text), tc.want))
		})
	}
}
