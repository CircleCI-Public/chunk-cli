package plan

import (
	"slices"
	"strings"
)

// Quote is how a comment on blocks quotes them: the span of text they came
// from, or, when it cannot be found there, their sources a blank line apart.
// The search starts after the first after in text, so that blocks whose
// markdown also appears earlier in the plan are found under their own
// heading; with after empty, or not in text, it starts at the beginning.
func Quote(text, after string, blocks []Block) string {
	src := sourcesOf(blocks)
	if len(src) == 0 {
		return ""
	}
	from := 0
	if after != "" {
		if i := strings.Index(text, after); i >= 0 {
			from = i + len(after)
		}
	}
	at := locate(text[from:], src)
	if n := len(src); !slices.Contains(at, -1) {
		return text[from+at[0] : from+at[n-1]+len(src[n-1])]
	}
	return strings.Join(src, "\n\n")
}

// sourcesOf is the markdown of blocks as the plan screen splits them: a list
// is its items, one source each.
func sourcesOf(blocks []Block) []string {
	var src []string
	var walk func(bs []Block)
	walk = func(bs []Block) {
		for _, b := range bs {
			if b.Type == "list" {
				walk(b.Blocks)
				continue
			}
			src = append(src, b.Text)
		}
	}
	walk(blocks)
	return src
}

// locate is where each of sources sits in text, searched for in order, each
// after the one before it, or -1 for one that is not found.
func locate(text string, sources []string) []int {
	at := make([]int, len(sources))
	from := 0
	for i, s := range sources {
		at[i] = -1
		if j := strings.Index(text[from:], s); j >= 0 {
			at[i], from = from+j, from+j+len(s)
		}
	}
	return at
}
