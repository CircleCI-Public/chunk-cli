package planstate

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestUnits(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []Unit
	}{
		{
			name: "no units section",
			text: "# P\n\n## 1. Outcome\n\n### Not a unit\n",
		},
		{
			name: "done and not",
			text: "## 5. Units\n\n### Unit 1: schema (done)\n\n### Unit 2: parser\n",
			want: []Unit{
				{
					Name: "Unit 1: schema",
					Done: true,
				},
				{
					Name: "Unit 2: parser",
				},
			},
		},
		{
			name: "deeper heading is not a unit",
			text: "## 5. Units\n\n### Unit 1: a\n\n#### Building\n",
			want: []Unit{
				{
					Name: "Unit 1: a",
				},
			},
		},
		{
			name: "fenced headings are not units",
			text: "## 5. Units\n\n### Unit 1: a\n\n" + "```md\n### Unit 9: example\n```\n" + "\n### Unit 2: b\n",
			want: []Unit{
				{
					Name: "Unit 1: a",
				},
				{
					Name: "Unit 2: b",
				},
			},
		},
		{
			name: "another units section is not read",
			text: "## 6. Unit notes\n\n### Not a unit\n",
		},
		{
			name: "done with a sha",
			text: "## 5. Units\n\n### Unit 1: a (done, 909706a)\n",
			want: []Unit{
				{
					Name: "Unit 1: a",
					Done: true,
				},
			},
		},
		{
			name: "a fence does not end the section",
			text: "## 5. Units\n\n### Unit 1: a\n\n" + "```md\n## 2. Design\n```\n" + "\n### Unit 2: b\n",
			want: []Unit{
				{
					Name: "Unit 1: a",
				},
				{
					Name: "Unit 2: b",
				},
			},
		},
		{
			name: "heading in another section",
			text: "## 2. Design\n\n### Context\n\n## 5. Units\n",
		},
		{
			name: "a parent's numbered list",
			text: "## 5. Units\n\n1. c1 retry-config: client\n2. c2 retry-docs: docs\n",
		},
		{
			name: "a top heading ends units",
			text: "## 5. Units\n\n### Unit 1: a\n\n# Appendix\n\n### Not a unit\n",
			want: []Unit{
				{
					Name: "Unit 1: a",
				},
			},
		},
		{
			name: "done only as a suffix",
			text: "## 5. Units\n\n### Unit 1: (done) early\n",
			want: []Unit{
				{
					Name: "Unit 1: (done) early",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.DeepEqual(Units(tc.text), tc.want))
		})
	}
}

// TestUnitsSkipAWorkedExamplesFencedUnits checks that a worked example's
// fenced Units section adds no units, and a unit's own subsections are not
// units.
func TestUnitsSkipAWorkedExamplesFencedUnits(t *testing.T) {
	text := "# A plan\n\n## 1. Outcome\n\nIt works.\n\n## 2. Design\n\n## 3. Open Questions\n\n" +
		"## 4. Worked example\n\n```md\n## 5. Units\n\n### Unit 1: example\n```\n\n" +
		"## 5. Units\n\n### Unit 1: parser (done)\n\nParse.\n\n#### Building\n\nCommitted.\n\n" +
		"### Unit 2: strip\n\nDraw.\n\n#### Building\n\nNot yet.\n"
	assert.Check(t, cmp.DeepEqual(Units(text), []Unit{
		{
			Name: "Unit 1: parser",
			Done: true,
		},
		{
			Name: "Unit 2: strip",
		},
	}))
}
