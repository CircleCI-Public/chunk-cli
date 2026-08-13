package jsonmerge

import (
	"encoding/json"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// Local types so the tests describe the merge rules rather than chunk's config
// schema, which is free to change.
type testStep struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type testEnv struct {
	Stack string     `json:"stack"`
	Setup []testStep `json:"setup"`
}

type testCommand struct {
	Name    string `json:"name"`
	Run     string `json:"run"`
	Timeout int    `json:"timeout,omitempty"`
}

type testConfig struct {
	Commands    []testCommand `json:"commands,omitempty"`
	OrgID       string        `json:"orgID,omitempty"`
	Environment *testEnv      `json:"environment,omitempty"`
}

func mergeInto(t *testing.T, model any, existing string) string {
	t.Helper()
	typed, err := json.Marshal(model)
	assert.NilError(t, err)
	got, err := Merge(typed, []byte(existing), model)
	assert.NilError(t, err)
	return string(got)
}

func TestMergePreservesUnknownTopLevelKey(t *testing.T) {
	got := mergeInto(t, &testConfig{OrgID: "org-1"}, `{"orgID":"old","myTool":{"x":1}}`)
	assert.Equal(t, got, `{"orgID":"org-1","myTool":{"x":1}}`)
}

// The reported case: keys hand-added to individual commands must survive a
// write that only touches something else.
func TestMergePreservesUnknownKeysOnArrayElements(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{
		{Name: "test", Run: "go test ./..."},
		{Name: "lint", Run: "golangci-lint run"},
	}}
	existing := `{"commands":[
	  {"name":"test","run":"go test ./...","fileExt":"go","limit":5,"always":true},
	  {"name":"lint","run":"golangci-lint run","fileExt":"go"}
	]}`

	got := mergeInto(t, cfg, existing)
	assert.Equal(t, got, `{"commands":[`+
		`{"name":"test","run":"go test ./...","fileExt":"go","limit":5,"always":true},`+
		`{"name":"lint","run":"golangci-lint run","fileExt":"go"}]}`)
}

func TestMergeMatchesArrayElementsByNameNotPosition(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{
		{Name: "lint", Run: "golangci-lint run"},
		{Name: "test", Run: "go test ./..."},
	}}
	existing := `{"commands":[{"name":"test","fileExt":"go"},{"name":"lint","limit":3}]}`

	got := mergeInto(t, cfg, existing)
	assert.Equal(t, got, `{"commands":[`+
		`{"name":"lint","run":"golangci-lint run","limit":3},`+
		`{"name":"test","run":"go test ./...","fileExt":"go"}]}`)
}

func TestMergeDropsUnknownKeysOfRemovedElement(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{{Name: "test", Run: "go test"}}}
	existing := `{"commands":[{"name":"test","run":"go test"},{"name":"lint","fileExt":"go"}]}`

	got := mergeInto(t, cfg, existing)
	assert.Equal(t, got, `{"commands":[{"name":"test","run":"go test"}]}`)
}

// An element only the file has is never resurrected. This is what keeps a setup
// step the caller deliberately dropped from coming back.
func TestMergeNeverResurrectsDroppedArrayElement(t *testing.T) {
	cfg := &testConfig{Environment: &testEnv{
		Stack: "go",
		Setup: []testStep{{Name: "install", Command: "go mod download"}},
	}}
	existing := `{"environment":{"stack":"go","setup":[
	  {"name":"install","command":"go mod download","cache":true},
	  {"name":"test","command":"go test ./..."}
	]}}`

	got := mergeInto(t, cfg, existing)
	assert.Equal(t, got, `{"environment":{"stack":"go","setup":[`+
		`{"name":"install","command":"go mod download","cache":true}]}}`)
}

// The delete half of the contract: a modeled key the typed document omits is
// removed, which is how clearing a value persists.
func TestMergeDeletesModeledKeyTypedOmits(t *testing.T) {
	got := mergeInto(t, &testConfig{}, `{"orgID":"org-1","keepMe":"yes"}`)
	assert.Equal(t, got, `{"keepMe":"yes"}`)
}

func TestMergeOrdersModeledKeysFirstThenUnknownInFileOrder(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{{Name: "test", Run: "go test"}}, OrgID: "org-1"}
	existing := `{"zeta":1,"orgID":"old","alpha":2,"commands":[]}`

	got := mergeInto(t, cfg, existing)
	assert.Equal(t, got, `{"commands":[{"name":"test","run":"go test"}],"orgID":"org-1","zeta":1,"alpha":2}`)
}

// Preserved values are copied as bytes. Decoding through interface{} would
// rewrite these numbers.
func TestMergePreservesNumberFidelity(t *testing.T) {
	existing := `{"big":1000000,"round":1.0,"exp":1e3,"huge":123456789012345678901,"neg":-0.5}`
	got := mergeInto(t, &testConfig{}, existing)
	assert.Equal(t, got, existing)
}

func TestMergePreservesStringFidelity(t *testing.T) {
	existing := `{"shell":"go build && go test","raw":"a && b","lt":"<x>","nl":"a\nb","emoji":"🚀"}`
	got := mergeInto(t, &testConfig{}, existing)
	assert.Equal(t, got, existing)
}

func TestMergeTypedWinsOnShapeMismatch(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{"commands is an object", `{"commands":{"nested":true}}`, `{"commands":[{"name":"test","run":"go test"}]}`},
		{"commands is null", `{"commands":null}`, `{"commands":[{"name":"test","run":"go test"}]}`},
		{"commands is a string", `{"commands":"nope"}`, `{"commands":[{"name":"test","run":"go test"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &testConfig{Commands: []testCommand{{Name: "test", Run: "go test"}}}
			assert.Equal(t, mergeInto(t, cfg, tt.existing), tt.want)
		})
	}
}

func TestMergeNonObjectExistingDocument(t *testing.T) {
	for _, existing := range []string{`[]`, `"x"`, `null`, `3`, `[{"name":"test"}]`} {
		t.Run(existing, func(t *testing.T) {
			got := mergeInto(t, &testConfig{OrgID: "org-1"}, existing)
			assert.Equal(t, got, `{"orgID":"org-1"}`)
		})
	}
}

func TestMergeDuplicateExistingKeyKeepsFirstPositionLastValue(t *testing.T) {
	got := mergeInto(t, &testConfig{}, `{"dup":1,"other":2,"dup":3}`)
	assert.Equal(t, got, `{"dup":3,"other":2}`)
}

func TestMergeArrayElementWithoutName(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{{Run: "go test"}}}
	got := mergeInto(t, cfg, `{"commands":[{"run":"go test","fileExt":"go"}]}`)
	// With nothing to pair on, the typed element is written as is.
	assert.Equal(t, got, `{"commands":[{"name":"","run":"go test"}]}`)
}

func TestMergeIsIdempotent(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{{Name: "test", Run: "go test"}}, OrgID: "org-1"}
	existing := `{"commands":[{"name":"test","run":"go test","fileExt":"go"}],"orgID":"org-1","extra":{"a":[1,2]}}`

	first := mergeInto(t, cfg, existing)
	second := mergeInto(t, cfg, first)
	assert.Equal(t, second, first)
}

func TestMergeNoUnknownKeysLeavesTypedUnchanged(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{{Name: "test", Run: "go test", Timeout: 30}}}
	typed, err := json.Marshal(cfg)
	assert.NilError(t, err)

	got, err := Merge(typed, typed, cfg)
	assert.NilError(t, err)
	assert.Equal(t, string(got), string(typed))
}

func TestMergeRejectsInvalidExistingDocument(t *testing.T) {
	for _, existing := range []string{"", "{", `{"a":}`, `{"a":1}trailing`} {
		t.Run(existing, func(t *testing.T) {
			_, err := Merge([]byte(`{}`), []byte(existing), &testConfig{})
			assert.ErrorIs(t, err, ErrInvalidJSON)
		})
	}
}

func TestUnknownKeys(t *testing.T) {
	data := `{
	  "orgID": "org-1",
	  "myTool": {"x": 1},
	  "commands": [
	    {"name":"test","fileExt":"go","limit":5},
	    {"name":"lint","fileExt":"go","always":true}
	  ],
	  "environment": {"stack":"go","setup":[{"name":"install","cache":true}]}
	}`

	got := UnknownKeys([]byte(data), &testConfig{})
	assert.DeepEqual(t, got, []string{
		"commands[].always",
		"commands[].fileExt",
		"commands[].limit",
		"environment.setup[].cache",
		"myTool",
	})
}

// Merge cannot pair an array element that has no name, so its unknown keys are
// dropped on save. Reporting them would promise a preservation that never
// happens.
func TestUnknownKeysSkipsUnnamedArrayElements(t *testing.T) {
	data := `{"commands":[{"run":"go test","fileExt":"go"},{"name":"lint","limit":5}]}`

	assert.DeepEqual(t, UnknownKeys([]byte(data), &testConfig{}), []string{"commands[].limit"})

	// The same document through Merge: the unnamed element keeps nothing.
	cfg := &testConfig{Commands: []testCommand{{Run: "go test"}, {Name: "lint", Run: "golangci-lint run"}}}
	got := mergeInto(t, cfg, data)
	assert.Equal(t, got, `{"commands":[{"name":"","run":"go test"},`+
		`{"name":"lint","run":"golangci-lint run","limit":5}]}`)
}

func TestUnknownKeysNoneForModeledDocument(t *testing.T) {
	cfg := &testConfig{Commands: []testCommand{{Name: "test", Run: "go test"}}, OrgID: "org-1"}
	data, err := json.Marshal(cfg)
	assert.NilError(t, err)
	assert.Equal(t, len(UnknownKeys(data, cfg)), 0)
}

func TestUnknownKeysInvalidDocument(t *testing.T) {
	assert.Equal(t, len(UnknownKeys([]byte("{"), &testConfig{})), 0)
}

// Types that marshal themselves must terminate the walk: their Go fields are
// not JSON keys, so walking them would treat real data as unknown.
func TestSchemaStopsAtSelfMarshalingTypes(t *testing.T) {
	type withTime struct {
		At time.Time `json:"at"`
	}
	data := `{"at":"2026-08-12T00:00:00Z"}`
	assert.Equal(t, len(UnknownKeys([]byte(data), &withTime{})), 0)

	got := mergeInto(t, &withTime{}, data)
	assert.Equal(t, got, `{"at":"0001-01-01T00:00:00Z"}`)
}

func TestSchemaTagHandling(t *testing.T) {
	type embedded struct {
		Inner string `json:"inner,omitempty"`
	}
	type tagged struct {
		embedded
		Renamed    string `json:"renamed,omitempty"`
		Untagged   string
		Skipped    string `json:"-"`
		unexported string //nolint:unused // present to prove it is skipped
	}

	data := `{"inner":"a","renamed":"b","Untagged":"c","Skipped":"d","extra":"e"}`
	got := UnknownKeys([]byte(data), &tagged{})
	// "inner" is inlined by the embedded struct, "Untagged" defaults to the Go
	// field name, and "Skipped" is not serialized so the file's copy is unknown.
	assert.DeepEqual(t, got, []string{"Skipped", "extra"})
}
