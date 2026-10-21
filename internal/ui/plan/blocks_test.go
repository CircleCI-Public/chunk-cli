package plan

import (
	"reflect"
	"testing"
)

func TestSplitBlocks(t *testing.T) {
	md := `# Add a flag

## 1. Outcome

The flag exists
and is documented.

## 2. Spec

* [USER] one
  * tradeoff
* [AGENT] two

  more about two
1. first
2. second

` + "```go\nfunc x() {}\n\nfunc y() {}\n```" + `

| a | b |
|---|---|
| 1 | 2 |
---
> quoted
`
	var got []string
	for _, b := range splitBlocks(md) {
		got = append(got, b.source)
	}
	want := []string{
		"# Add a flag",
		"## 1. Outcome",
		"The flag exists\nand is documented.",
		"## 2. Spec",
		"* [USER] one\n  * tradeoff",
		"* [AGENT] two\n\n  more about two",
		"1. first",
		"2. second",
		"```go\nfunc x() {}\n\nfunc y() {}\n```",
		"| a | b |\n|---|---|\n| 1 | 2 |",
		"---",
		"> quoted",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blocks:\n%q\nwant:\n%q", got, want)
	}
}
