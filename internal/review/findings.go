package review

import (
	"encoding/json"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits on what is accepted from a model's structured output. They exist
// because the output is untrusted text: a runaway or manipulated review must not
// be able to make the daemon hold or post an unbounded amount.
const (
	// MaxFindings caps the findings kept from one review.
	MaxFindings = 50
	// maxFindingBody caps one finding's explanation, in runes.
	maxFindingBody = 4000
	// maxFindingPatch caps one finding's suggested patch, in bytes.
	maxFindingPatch = 20000
)

// Severities a finding can carry, most serious first.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
	SeverityInfo   = "info"
)

// Finding is one issue a review reports, in a form a program can act on: where
// it is, how serious it is, what is wrong, and optionally how to fix it.
type Finding struct {
	// ID is unique within a run. The daemon assigns it; a review's own output
	// does not carry one.
	ID string `json:"id,omitempty"`
	// Prompt names the review that reported it.
	Prompt string `json:"prompt,omitempty"`
	// File is a repository-relative path with forward slashes.
	File string `json:"file"`
	// Line is the 1-based line the finding is about; zero means the file as a
	// whole or a line the reviewer did not give.
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
	// Patch is an optional unified diff that would fix the finding.
	Patch string `json:"patch,omitempty"`
}

// FindingsInstructions is appended to a prompt when structured findings are
// wanted. The prose review the prompt asks for is unchanged; the block is an
// addition, and everything downstream copes with it being absent.
const FindingsInstructions = "\n\n---\n" +
	"After your review, end your answer with ONE fenced code block tagged json holding a single object:\n\n" +
	"```json\n" +
	`{"findings":[{"file":"path/relative/to/the/repo/root","line":42,"severity":"high|medium|low|info","body":"what is wrong and why","patch":"optional unified diff that fixes it"}]}` + "\n" +
	"```\n\n" +
	"Use an empty findings array when there is nothing to report. Report only issues in the code under review. " +
	"Put the JSON in that one block and nowhere else."

// Parsed is the result of reading structured findings out of a review's output.
type Parsed struct {
	// Findings are the valid findings, in the order given, at most MaxFindings.
	Findings []Finding
	// Found reports that a findings object was located and decoded, even if it
	// held no findings. False means the review has prose only, which is a normal
	// outcome and not an error.
	Found bool
	// Dropped counts entries that were present but unusable (no file, no body, a
	// path outside the repository) or over the cap.
	Dropped int
	// Prose is the output with the findings block removed. When nothing was
	// found it is the output unchanged.
	Prose string
}

// ParseFindings extracts structured findings from a review's output, defensively.
//
// It never fails: a review that ignored the instructions, wrapped the JSON in
// chatter, or produced malformed JSON simply yields Found false and its prose.
// Of several candidate blocks the last decodable one wins, since a model that
// revised its answer puts the final version last.
func ParseFindings(output string) Parsed {
	res := Parsed{Prose: output}
	for _, b := range candidateBlocks(output) {
		raw, ok := decodeFindings(b.text)
		if !ok {
			continue
		}
		res.Found = true
		res.Findings, res.Dropped = cleanFindings(raw)
		res.Prose = strings.TrimSpace(output[:b.start] + output[b.end:])
		return res
	}
	return res
}

// block is a candidate JSON region: its text and where it sits in the output,
// so the prose can be cut around it.
type block struct {
	text       string
	start, end int
}

// maxClosersTried bounds how many later fences are tried as the end of one
// opening fence.
const maxClosersTried = 8

// candidateBlocks lists the regions that might hold the findings object, in the
// order ParseFindings should try them: openers last to first, and for each
// opener its nearest closing fence first.
//
// Trying more than the nearest closer is what lets a finding whose text contains
// a code fence (a suggested patch that adds a markdown block) survive: the fence
// inside the JSON string looks like the end of the block, so the nearest closer
// cuts the JSON in half and only a later one yields text that decodes. As a last
// resort the whole output is tried when it is itself JSON.
func candidateBlocks(output string) []block {
	var fences []int
	for i := 0; i < len(output); {
		j := strings.Index(output[i:], "```")
		if j < 0 {
			break
		}
		fences = append(fences, i+j)
		i += j + 3
	}

	var blocks []block
	for o := len(fences) - 1; o >= 0; o-- {
		open := fences[o]
		afterOpen := open + 3
		nl := strings.IndexByte(output[afterOpen:], '\n')
		if nl < 0 {
			continue
		}
		lang := strings.ToLower(strings.TrimSpace(output[afterOpen : afterOpen+nl]))
		if lang != "json" && lang != "" {
			continue
		}
		bodyStart := afterOpen + nl + 1
		tried := 0
		for c := o + 1; c < len(fences) && tried < maxClosersTried; c++ {
			if fences[c] < bodyStart {
				continue
			}
			tried++
			blocks = append(blocks, block{
				text:  output[bodyStart:fences[c]],
				start: open,
				end:   fences[c] + 3,
			})
		}
	}
	if trimmed := strings.TrimSpace(output); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		blocks = append(blocks, block{text: trimmed, start: 0, end: len(output)})
	}
	return blocks
}

// rawFinding accepts the shapes models actually produce: a line as a number or a
// numeric string, and any field missing.
type rawFinding struct {
	File     string          `json:"file"`
	Line     json.RawMessage `json:"line"`
	Severity string          `json:"severity"`
	Body     string          `json:"body"`
	Patch    string          `json:"patch"`
}

// decodeFindings decodes either {"findings":[...]} or a bare array. ok is false
// when the text is neither, so a JSON block that is about something else (a
// config example the review quoted) is not mistaken for findings.
func decodeFindings(text string) ([]rawFinding, bool) {
	text = strings.TrimSpace(text)
	var wrapped struct {
		Findings *[]rawFinding `json:"findings"`
	}
	if strings.HasPrefix(text, "{") {
		if err := json.Unmarshal([]byte(text), &wrapped); err != nil || wrapped.Findings == nil {
			return nil, false
		}
		return *wrapped.Findings, true
	}
	var bare []rawFinding
	if strings.HasPrefix(text, "[") {
		if err := json.Unmarshal([]byte(text), &bare); err != nil {
			return nil, false
		}
		return bare, true
	}
	return nil, false
}

func cleanFindings(raw []rawFinding) ([]Finding, int) {
	var out []Finding
	dropped := 0
	for _, r := range raw {
		if len(out) >= MaxFindings {
			dropped++
			continue
		}
		f, ok := cleanFinding(r)
		if !ok {
			dropped++
			continue
		}
		out = append(out, f)
	}
	return out, dropped
}

func cleanFinding(r rawFinding) (Finding, bool) {
	file, ok := cleanRepoPath(r.File)
	body := truncateRunes(strings.TrimSpace(r.Body), maxFindingBody)
	if !ok || body == "" {
		return Finding{}, false
	}
	patch := r.Patch
	if len(patch) > maxFindingPatch {
		// A truncated diff would not apply; better none than a broken one.
		patch = ""
	}
	return Finding{
		File:     file,
		Line:     parseLine(r.Line),
		Severity: normalizeSeverity(r.Severity),
		Body:     body,
		Patch:    patch,
	}, true
}

// cleanRepoPath accepts only a path inside the repository. Findings become
// review comments and edit targets, so a path naming somewhere else (absolute,
// or climbing out with "..") is refused rather than passed on.
func cleanRepoPath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func parseLine(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return max(n, 0)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, convErr := strconv.Atoi(strings.TrimSpace(s)); convErr == nil {
			return max(n, 0)
		}
	}
	return 0
}

// normalizeSeverity maps the words models reach for onto the four levels, and
// anything unrecognised to info: a finding of unknown weight should not be
// promoted to something that looks urgent.
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high", "critical", "error", "blocker", "major":
		return SeverityHigh
	case "medium", "moderate", "warning", "warn":
		return SeverityMedium
	case "low", "minor", "nit", "nitpick":
		return SeverityLow
	}
	return SeverityInfo
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// WorthChanging reports whether a finding is serious enough to act on: severity
// high or medium. Lower findings are kept on the record but never trigger a fix,
// so a loop does not churn the code over style remarks.
func (f Finding) WorthChanging() bool {
	return f.Severity == SeverityHigh || f.Severity == SeverityMedium
}

// DedupeFindings drops repeats: several reviews of the same change often flag
// the same line for the same reason, and it need not be fixed twice. The first
// copy's ID is kept, at the most serious severity any copy had.
func DedupeFindings(findings []Finding) []Finding {
	type key struct {
		file string
		line int
		body string
	}
	rank := map[string]int{SeverityHigh: 0, SeverityMedium: 1, SeverityLow: 2, SeverityInfo: 3}
	index := map[key]int{}
	var out []Finding
	for _, f := range findings {
		k := key{f.File, f.Line, strings.ToLower(strings.Join(strings.Fields(f.Body), " "))}
		if i, seen := index[k]; seen {
			if rank[f.Severity] < rank[out[i].Severity] {
				out[i].Severity = f.Severity
			}
			continue
		}
		index[k] = len(out)
		out = append(out, f)
	}
	return out
}
