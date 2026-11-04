package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
)

func TestSplitBlocks(t *testing.T) {
	t.Parallel()
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

// TestRenderLinesDrawsLinksAndHeadings guards glamour's rendering of the
// markdown that goldmark's link reference definitions and heading positions
// touch: a reference-style link, a setext heading and an inline link.
func TestRenderLinesDrawsLinksAndHeadings(t *testing.T) {
	t.Parallel()
	md := "Setext heading\n==============\n\n" +
		"See [x][r] and [inline](https://inline.example.com).\n\n" +
		"[r]: https://example.com\n"
	r, err := chat.NewRenderer(true, 80)
	assert.NilError(t, err)
	out := ansi.Strip(strings.Join(renderLines(r, md), "\n"))
	t.Log("\n" + out)
	for _, want := range []string{"Setext heading", "x", "https://example.com", "inline", "https://inline.example.com"} {
		assert.Check(t, cmp.Contains(out, want))
	}
	assert.Check(t, !strings.Contains(out, "[r]:"), "definition line drawn:\n%s", out)
}

// TestRenderLinesIsStableAcrossGoldmarkReleases pins what glamour draws for
// markdown that goldmark v1.7.8 through v1.8.6 changed the parser or tree for:
// link reference definitions (now tree nodes), ATX heading closing sequences,
// GFM autolinks, table delimiter rows, list and blank line handling, code
// fences, HTML blocks, thematic breaks and the line reader behind them. Each
// case draws the same bytes under both versions. A case that changes after a
// goldmark bump is a change in what glamour draws. want is the drawn text
// without styling, trailing blanks dropped.
func TestRenderLinesIsStableAcrossGoldmarkReleases(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, md, want string }{
		{
			name: "reference definitions then a blank line",
			md: "[a]: /url\n" +
				"[b]: /url2 \"title\"\n" +
				"\n" +
				"Use [a] and [b] and [a][] and [text][b] and ![img][a].\n",
			want: "  Use a /url and b /url2 and a /url and text /url2 and Image: img → /url.",
		},
		{
			name: "reference definition split over lines",
			md: "[Foo bar]:\n" +
				"<my url>\n" +
				"'title'\n" +
				"\n" +
				"[foo bar]\n",
			want: "  foo bar /my%20url",
		},
		{
			name: "reference definition is not a paragraph's",
			md: "para\n" +
				"[r]: /x\n" +
				"more\n" +
				"\n" +
				"See [r].\n",
			want: "  para [r]: /x more\n" +
				"\n" +
				"  See [r].",
		},
		{
			name: "reference definition in a list item",
			md: "- [a]: /b\n" +
				"\n" +
				"  text\n" +
				"- item [a]\n",
			want: "  • text\n" +
				"  • item a /b",
		},
		{
			name: "reference definition alone in a quote",
			md:   "> [a]: /b\n",
			want: "",
		},
		{
			name: "reference definitions between blocks",
			md: "Intro\n" +
				"\n" +
				"[a]: /1\n" +
				"[b]: /2\n" +
				"\n" +
				"- [a]\n" +
				"- [b]\n" +
				"\n" +
				"```\n" +
				"[x]: /not\n" +
				"```\n",
			want: "  Intro\n" +
				"\n" +
				"  • a /1\n" +
				"  • b /2\n" +
				"\n" +
				"    [x]: /not",
		},
		{
			name: "ATX closing sequences",
			md: "# h1\n" +
				"\n" +
				"## h2 ##\n" +
				"\n" +
				"### h3 ###   \n" +
				"\n" +
				"#### h4 #####x\n" +
				"\n" +
				"##### h5#\n" +
				"\n" +
				"###### h6 \\##\n",
			want: "   h1\n" +
				"\n" +
				"  ## h2\n" +
				"\n" +
				"  ### h3\n" +
				"\n" +
				"  #### h4 #####x\n" +
				"\n" +
				"  ##### h5#\n" +
				"\n" +
				"  ###### h6 ##",
		},
		{
			name: "ATX edge headings",
			md: "## foo #\\##\n" +
				"\n" +
				"   ## indented\n" +
				"\n" +
				"    ## code\n" +
				"\n" +
				"#\tTabbed\n" +
				"\n" +
				"#5 not a heading\n" +
				"\n" +
				"####### seven\n",
			want: "  ## foo ###\n" +
				"\n" +
				"  ## indented\n" +
				"\n" +
				"    ## code\n" +
				"\n" +
				"   Tabbed\n" +
				"\n" +
				"  #5 not a heading\n" +
				"\n" +
				"  ####### seven",
		},
		{
			name: "ATX heading with inline markup",
			md: "## Heading with `code` and *em* ##\n" +
				"\n" +
				"# [l](/u) ###\n",
			want: "  ## Heading with \u00a0code\u00a0 and em\n" +
				"\n" +
				"   l /u",
		},
		{
			name: "setext headings",
			md: "Setext one\n" +
				"===\n" +
				"\n" +
				"Setext two\n" +
				"---\n" +
				"\n" +
				"multi\n" +
				"line\n" +
				"---\n",
			want: "   Setext one\n" +
				"\n" +
				"  ## Setext two\n" +
				"\n" +
				"  ## multi\n" +
				"  line",
		},
		{
			name: "links with balanced parentheses",
			md:   "https://example.com/a_(b) and (see https://example.com/a) end.\n",
			want: "  https://example.com/a_(b) and (see https://example.com/a) end.",
		},
		{
			name: "autolinks before punctuation",
			md:   "https://example.com. and https://example.com, and https://example.com; ok\n",
			want: "  https://example.com. and https://example.com, and https://example.com; ok",
		},
		{
			name: "autolink before an entity",
			md:   "visit https://example.com/path&amp; now https://example.com/?a=1&copy; ok\n",
			want: "  visit https://example.com/path& now https://example.com/?a=1© ok",
		},
		{
			name: "www and email autolinks",
			md:   "www.example.com) and (www.example.com) and a@b.com. or (a@b.com)\n",
			want: "  http://www.example.com) and (http://www.example.com) and a@b.com. or\n" +
				"  (a@b.com)",
		},
		{
			name: "inline, image and angle links",
			md: "[link](/u \"t\") ![i](/p \"t2\") <https://auto.example.com> <a@b.co>\n" +
				"\n" +
				"[a [nested] b](/u)\n" +
				"\n" +
				"[![img](/i)](/l)\n",
			want: "  link /u Image: i → /p https://auto.example.com a@b.co\n" +
				"\n" +
				"  a [nested] b /u\n" +
				"\n" +
				"  Image: img → /i /l",
		},
		{
			name: "table",
			md: "| a | b |\n" +
				"|---|---|\n" +
				"| 1 | 2 |\n",
			want: "   a                                    │ b\n" +
				"  ──────────────────────────────────────┼─────────────────────────────────────\n" +
				"   1                                    │ 2",
		},
		{
			name: "table with aligned columns and inline markup",
			md: "| a | b |\n" +
				"|:--|--:|\n" +
				"| `x|y` | \\| z |\n" +
				"| [l](/u) | **bold** |\n",
			want: "   a                                    │                                   b\n" +
				"  ──────────────────────────────────────┼─────────────────────────────────────\n" +
				"   `x                                   │                                  y`\n" +
				"   l[1]                                 │                                bold\n" +
				"\n" +
				"  [1]: l /u",
		},
		{
			name: "dash row under one line is a setext heading",
			md: "a\n" +
				"---\n",
			want: "  ## a",
		},
		{
			name: "table with one-dash delimiters",
			md: "| a |\n" +
				"| - |\n" +
				"| 1 |\n" +
				"\n" +
				"a | b\n" +
				"- | -\n" +
				"1 | 2\n",
			want: "   a\n" +
				"  ────────────────────────────────────────────────────────────────────────────\n" +
				"   1\n" +
				"\n" +
				"  a | b\n" +
				"\n" +
				"  • | -\n" +
				"  1 | 2",
		},
		{
			name: "nested lists",
			md: "- a\n" +
				"- b\n" +
				"  - c\n" +
				"    - d\n" +
				"- e\n" +
				"\n" +
				"1. a\n" +
				"2. b\n" +
				"   1. c\n" +
				"   2. d\n" +
				"3. e\n",
			want: "  • a\n" +
				"  • b\n" +
				"    • c\n" +
				"      • d\n" +
				"\n" +
				"  • e\n" +
				"\n" +
				"  1. a\n" +
				"  2. b\n" +
				"    1. c\n" +
				"    2. d\n" +
				"  3. e",
		},
		{
			name: "loose lists",
			md: "- a\n" +
				"\n" +
				"- b\n" +
				"\n" +
				"- c\n" +
				"\n" +
				"- a\n" +
				"  b\n" +
				"- c\n" +
				"\n" +
				"  d\n" +
				"- e\n",
			want: "  • a\n" +
				"  • b\n" +
				"  • c\n" +
				"  • a\n" +
				"  b\n" +
				"  • cd\n" +
				"  • e",
		},
		{
			name: "lists and lazy lines",
			md: "foo\n" +
				"    - bar\n" +
				"\n" +
				"foo\n" +
				"- bar\n" +
				"\n" +
				"foo\n" +
				"2. bar\n" +
				"1. baz\n",
			want: "  foo - bar\n" +
				"\n" +
				"  foo\n" +
				"\n" +
				"  • bar\n" +
				"\n" +
				"  foo 2. bar\n" +
				"\n" +
				"  1. baz",
		},
		{
			name: "list items with blocks",
			md: "- a\n" +
				"\n" +
				"    code\n" +
				"- b\n" +
				"\n" +
				"- ```\n" +
				"  code\n" +
				"\n" +
				"  more\n" +
				"  ```\n" +
				"- x\n" +
				"\n" +
				"- > quote\n" +
				"  > more\n" +
				"- y\n",
			want: "  • acode\n" +
				"  • b\n" +
				"  •\n" +
				"    code\n" +
				"\n" +
				"    more\n" +
				"\n" +
				"  • x\n" +
				"  •\n" +
				"  │ quote more\n" +
				"\n" +
				"  • y",
		},
		{
			name: "list item starting on the next line",
			md: "-\n" +
				"  foo\n" +
				"-\n" +
				"  bar\n" +
				"\n" +
				"- a\n" +
				"-\n" +
				"- c\n",
			want: "  • foo\n" +
				"  • bar\n" +
				"  • a\n" +
				"  •\n" +
				"  • c",
		},
		{
			name: "quote around lists",
			md: "> - a\n" +
				">   - b\n" +
				"> - c\n" +
				">\n" +
				"> text\n" +
				"\n" +
				"> a\n" +
				"\n" +
				"\n" +
				"> b\n",
			want: "  │\n" +
				"  │ • a\n" +
				"  │   • b\n" +
				"  │ • c\n" +
				"  │\n" +
				"  │ text\n" +
				"\n" +
				"  │ a\n" +
				"\n" +
				"  │ b",
		},
		{
			name: "task list",
			md: "- [ ] todo\n" +
				"- [x] done\n" +
				"  - [ ] sub\n",
			want: "  [ ] todo\n" +
				"  [✓] done\n" +
				"    [ ] sub",
		},
		{
			name: "list with blank lines and tabs",
			md: "- a\n" +
				"\t- b\n" +
				"\t\t- c\n" +
				"\n" +
				"- one\n" +
				"- two\n" +
				"\n" +
				"text after\n" +
				"\n" +
				"- three\n",
			want: "  • a\n" +
				"    • b\n" +
				"      • c\n" +
				"\n" +
				"  • one\n" +
				"  • two\n" +
				"\n" +
				"  text after\n" +
				"\n" +
				"  • three",
		},
		{
			name: "fenced code",
			md: "```go\n" +
				"func main() {}\n" +
				"```\n" +
				"\n" +
				"~~~\n" +
				"tilde\n" +
				"~~~\n" +
				"\n" +
				"````\n" +
				"```\n" +
				"nested\n" +
				"```\n" +
				"````\n",
			want: "    func main() {}\n" +
				"\n" +
				"    tilde\n" +
				"\n" +
				"    ```\n" +
				"    nested\n" +
				"    ```",
		},
		{
			name: "unclosed fence",
			md: "```\n" +
				"unclosed\n",
			want: "    unclosed",
		},
		{
			name: "fence with blank lines and tabs",
			md: "```\n" +
				"code with\ttab\n" +
				"\n" +
				"\n" +
				"blank lines\n" +
				"```\n" +
				"\n" +
				"```\n" +
				"\ttabbed\n" +
				"  \n" +
				"```\n",
			want: "    code with\ttab\n" +
				"\n" +
				"\n" +
				"    blank lines\n" +
				"\n" +
				"    \ttabbed",
		},
		{
			name: "fenced code in item, quote and indented",
			md: "- ```\n" +
				"  fenced in item\n" +
				"  ```\n" +
				"\n" +
				"> ```\n" +
				"> quote fence\n" +
				"> ```\n" +
				"\n" +
				"  ```\n" +
				"  indented fence\n" +
				"   keep\n" +
				"  ```\n",
			want: "  •\n" +
				"    fenced in item\n" +
				"\n" +
				"\n" +
				"  │\n" +
				"  │   quote fence\n" +
				"\n" +
				"    indented fence\n" +
				"     keep",
		},
		{
			name: "indented code",
			md: "    indented code\n" +
				"    more\n" +
				"\n" +
				"    after blank\n" +
				"\n" +
				"\tTab code\n" +
				"\tmore\n" +
				"\n" +
				">     in a quote\n",
			want: "    indented code\n" +
				"    more\n" +
				"\n" +
				"    after blank\n" +
				"\n" +
				"    Tab code\n" +
				"    more\n" +
				"\n" +
				"  │\n" +
				"  │   in a quote",
		},
		{
			name: "HTML blocks",
			md: "<div>\n" +
				"hello\n" +
				"</div>\n" +
				"\n" +
				"<!-- comment -->\n" +
				"\n" +
				"text\n" +
				"\n" +
				"<pre>\n" +
				"  raw\n" +
				"</pre>\n" +
				"\n" +
				"after\n",
			want: "  hello\n" +
				"  text\n" +
				"  raw\n" +
				"  after",
		},
		{
			name: "HTML block kinds",
			md: "<script>\n" +
				"var x;\n" +
				"</script>\n" +
				"\n" +
				"<?php echo 1; ?>\n" +
				"\n" +
				"<!DOCTYPE html>\n" +
				"\n" +
				"<![CDATA[x]]>\n",
			want: "",
		},
		{
			name: "HTML block indentation and containers",
			md: "  <div>indented</div>\n" +
				"\n" +
				"    <div>code</div>\n" +
				"\n" +
				"- <div>in item</div>\n" +
				"\n" +
				"> <div>in quote</div>\n",
			want: "  indented\n" +
				"    <div>code</div>\n" +
				"\n" +
				"  • in item\n" +
				"\n" +
				"  │ in quote",
		},
		{
			name: "thematic breaks",
			md: "a\n" +
				"\n" +
				"***\n" +
				"\n" +
				"b\n" +
				"\n" +
				"___\n" +
				"\n" +
				"c\n" +
				"\n" +
				"- - -\n" +
				"\n" +
				"d\n" +
				"\n" +
				" * * *\n" +
				"\n" +
				"- a\n" +
				"- - -\n" +
				"- b\n" +
				"\n" +
				"foo\n" +
				"***\n" +
				"bar\n",
			want: "  a\n" +
				"\n" +
				"  --------\n" +
				"\n" +
				"  b\n" +
				"\n" +
				"  --------\n" +
				"\n" +
				"  c\n" +
				"\n" +
				"  --------\n" +
				"\n" +
				"  d\n" +
				"\n" +
				"  --------\n" +
				"\n" +
				"  • a\n" +
				"\n" +
				"  --------\n" +
				"\n" +
				"  • b\n" +
				"\n" +
				"  foo\n" +
				"\n" +
				"  --------\n" +
				"\n" +
				"  bar",
		},
		{
			name: "paragraph whitespace and breaks",
			md: "line one  \n" +
				"line two\\\n" +
				"line three\n" +
				"line four\n" +
				"\n" +
				"   leading\n" +
				"   spaces\n" +
				"\n" +
				"  more\n" +
				"\n" +
				"para\n" +
				"   \n" +
				"after spaces-only line\n",
			want: "  line one line two line three line four\n" +
				"\n" +
				"  leading spaces\n" +
				"\n" +
				"  more\n" +
				"\n" +
				"  para\n" +
				"\n" +
				"  after spaces-only line",
		},
		{
			name: "CRLF line endings",
			md: "# h\r\n" +
				"\r\n" +
				"- a\r\n" +
				"- b\r\n" +
				"\r\n" +
				"```\r\n" +
				"code\r\n" +
				"```\r\n" +
				"\r\n" +
				"a\r\n" +
				"b\r\n",
			want: "   h\n" +
				"\n" +
				"  • a\n" +
				"  • b\n" +
				"\n" +
				"    code\n" +
				"\n" +
				"  a b",
		},
		{
			name: "no trailing newline",
			md:   "no trailing newline",
			want: "  no trailing newline",
		},
		{
			name: "emphasis",
			md: "*em* **strong** ***both*** _em_ __strong__ ~~del~~ `code` ``co`de``\n" +
				"\n" +
				"**a *b* c** and *a **b** c* and a*b*c and snake_case_word and 2*3*4\n" +
				"\n" +
				"*a **b***\n" +
				"\n" +
				"**a *b***\n" +
				"\n" +
				"*foo**bar**baz*\n",
			want: "  em strong both em strong del \u00a0code\u00a0 \u00a0co`de\u00a0\n" +
				"\n" +
				"  a b c and a b c and abc and snake_case_word and 234\n" +
				"\n" +
				"  a b\n" +
				"\n" +
				"  a b\n" +
				"\n" +
				"  foobarbaz",
		},
		{
			name: "escapes, entities and emoji",
			md: "emoji :smile: and :+1:\n" +
				"\n" +
				"escape \\* \\_ \\# \\[ \\] \\\\ \\` and &amp; &lt; &copy; &#35; &nbsp;\n",
			want: "  emoji :smile: and :+1:\n" +
				"\n" +
				"  escape * _ # [ ] \\ ` and & < © # \u00a0",
		},
		{
			name: "definition lists",
			md: "Term\n" +
				": Definition\n" +
				": Another\n" +
				"\n" +
				"Term 2\n" +
				": Def 2\n" +
				"\n" +
				"Term *em*\n" +
				": Def with **strong**\n" +
				"\n" +
				"    para\n",
			want: "  Term\n" +
				"  🠶 Definition\n" +
				"\n" +
				"  🠶 Another\n" +
				"\n" +
				"  Term 2\n" +
				"  🠶 Def 2\n" +
				"\n" +
				"  Term em\n" +
				"  🠶 Def with strong\n" +
				"  para",
		},
		{
			name: "non-ASCII link destinations",
			md:   "[u](/日本語/é?x=ü) and [v](/a%20b) and [w](</a b>)\n",
			want: "  語/é?x=ü\au 語/é?x=ü\a/%E6%97%A5%E6%9C%AC%E8%AA%9E/%C3%A9?x=ü and v /a%20b and\n" +
				"  w /a%20b",
		},
		{
			name: "a plan",
			md: "## 1. Outcome\n" +
				"\n" +
				"One parser. See [docs][d] and https://example.com/a_(b).\n" +
				"\n" +
				"[d]: https://docs.example.com \"Docs\"\n" +
				"\n" +
				"### Unit 1: parser\n" +
				"\n" +
				"- Add `Parse`.\n" +
				"- Test it.\n" +
				"  - nested\n" +
				"\n" +
				"```go\n" +
				"func route() {}\n" +
				"```\n" +
				"\n" +
				"| a | b |\n" +
				"|---|---|\n" +
				"| 1 | 2 |\n" +
				"\n" +
				"---\n" +
				"\n" +
				"> quoted\n" +
				"> more\n" +
				"\n" +
				"1. first\n" +
				"2. second\n",
			want: "  ## 1. Outcome\n" +
				"\n" +
				"  One parser. See docs https://docs.example.com and https://example.com/a_(b).\n" +
				"\n" +
				"  ### Unit 1: parser\n" +
				"\n" +
				"  • Add \u00a0Parse\u00a0.\n" +
				"  • Test it.\n" +
				"    • nested\n" +
				"\n" +
				"\n" +
				"    func route() {}\n" +
				"\n" +
				"   a                                    │ b\n" +
				"  ──────────────────────────────────────┼─────────────────────────────────────\n" +
				"   1                                    │ 2\n" +
				"\n" +
				"  --------\n" +
				"\n" +
				"  │ quoted more\n" +
				"\n" +
				"  1. first\n" +
				"  2. second",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := chat.NewRenderer(true, 80)
			assert.NilError(t, err)
			var got []string
			for _, l := range renderLines(r, tt.md) {
				got = append(got, strings.TrimRight(ansi.Strip(l), " "))
			}
			assert.Check(t, cmp.Equal(strings.Join(got, "\n"), tt.want))
		})
	}
}
