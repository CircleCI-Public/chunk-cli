package clipboard

import (
	"slices"
	"testing"

	"gotest.tools/v3/assert"
)

func TestToolIsChosenByTheSystemAndItsDisplay(t *testing.T) {
	installed := func(names ...string) func(string) bool {
		return func(n string) bool { return slices.Contains(names, n) }
	}
	env := func(pairs ...string) func(string) string {
		return func(k string) string {
			for i := 0; i < len(pairs); i += 2 {
				if pairs[i] == k {
					return pairs[i+1]
				}
			}
			return ""
		}
	}
	tests := []struct {
		name string
		goos string
		has  func(string) bool
		env  func(string) string
		want []string
	}{
		{"mac", "darwin", installed("pbcopy"), env(), []string{"pbcopy"}},
		{"mac without the tool", "darwin", installed(), env(), nil},
		{"windows", "windows", installed("clip.exe"), env(), []string{"clip.exe"}},
		{"wayland", "linux", installed("wl-copy", "xclip"), env("WAYLAND_DISPLAY", "wayland-0", "DISPLAY", ":0"), []string{"wl-copy"}},
		{"wayland without wl-copy falls back to x", "linux", installed("xclip"), env("WAYLAND_DISPLAY", "wayland-0", "DISPLAY", ":0"),
			[]string{"xclip", "-selection", "clipboard"}},
		{"x with xclip", "linux", installed("xclip", "xsel"), env("DISPLAY", ":0"), []string{"xclip", "-selection", "clipboard"}},
		{"x with only xsel", "linux", installed("xsel"), env("DISPLAY", ":0"), []string{"xsel", "--clipboard", "--input"}},
		{"linux with no display", "linux", installed("xclip", "wl-copy"), env(), nil},
		{"over ssh the machine's clipboard is not the developer's", "darwin", installed("pbcopy"), env("SSH_CONNECTION", "1 2 3 4"), nil},
		{"over ssh with a tty", "linux", installed("xclip"), env("SSH_TTY", "/dev/pts/1", "DISPLAY", ":0"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, tool(tt.goos, tt.has, tt.env), tt.want)
		})
	}
}
