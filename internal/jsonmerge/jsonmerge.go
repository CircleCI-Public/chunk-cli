// Package jsonmerge splices keys from an existing JSON document back into a
// document produced by marshaling a typed Go value, so that rewriting a config
// file through a struct does not delete keys the struct does not model.
//
// Values are carried across verbatim as raw bytes. Decoding into
// map[string]interface{} would turn every number into a float64 and rewrite
// 1000000 as 1e+06, so the merge never round-trips a value it is only meant to
// preserve.
package jsonmerge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// matchKey is the object key used to pair up array elements across the two
// documents. It is the identity chunk itself uses for the arrays in its config:
// validation commands and environment setup steps are both named.
const matchKey = "name"

// ErrInvalidJSON marks an existing document that could not be parsed. Callers
// that are about to overwrite that document use it to refuse instead.
var ErrInvalidJSON = errors.New("not valid JSON")

// Merge returns typed with every object key that model's type does not model
// re-attached from existing. Both arguments are JSON documents; the result is
// compact JSON, so formatting is the caller's concern.
//
// The rules:
//   - A key model's type models comes from typed alone. A key it models but that
//     typed omits — an empty value under omitempty, or one the caller cleared —
//     is deleted rather than restored from existing.
//   - A key model's type does not model is copied from existing byte for byte,
//     placed after the modeled keys of the same object in the order existing had
//     them.
//   - Arrays of structs are paired element-wise by the value of matchKey, and
//     only the elements typed has survive. An element that exists only in
//     existing is never resurrected, so a dropped array entry stays dropped
//     while the unknown keys of the entries that remain travel back.
//   - Where the two documents disagree in shape, typed wins.
//
// model is inspected by type only; its value is ignored.
func Merge(typed, existing []byte, model any) ([]byte, error) {
	// A truncated document would half-parse and let typed through silently,
	// hiding from the caller that it is about to overwrite a broken file.
	if !json.Valid(existing) {
		return nil, ErrInvalidJSON
	}
	root := schemaOf(reflect.TypeOf(model))
	if root == nil {
		return typed, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, mergeValue(typed, existing, root)); err != nil {
		return nil, fmt.Errorf("compact merged document: %w", err)
	}
	return buf.Bytes(), nil
}

// UnknownKeys returns the paths in data that model's type does not model,
// sorted and deduplicated. Paths join object keys with "." and mark array
// elements with "[]", so an unmodeled key on a command reads as
// "commands[].fileExt". Merge preserves these keys; this reports them so a
// caller can point out a typo instead of keeping it forever. Only keys Merge
// would actually carry across are reported, so array elements Merge cannot pair
// are skipped here too.
func UnknownKeys(data []byte, model any) []string {
	root := schemaOf(reflect.TypeOf(model))
	if root == nil || !json.Valid(data) {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	collectUnknown(data, root, "", seen, &paths)
	sort.Strings(paths)
	return paths
}

func collectUnknown(data json.RawMessage, n *node, prefix string, seen map[string]bool, out *[]string) {
	switch {
	case n == nil:
		return
	case n.fields != nil:
		members, ok := parseObject(data)
		if !ok {
			return
		}
		for _, m := range members {
			path := joinPath(prefix, m.key)
			child, modeled := n.fields[m.key]
			if !modeled {
				if !seen[path] {
					seen[path] = true
					*out = append(*out, path)
				}
				continue
			}
			collectUnknown(m.val, child, path, seen, out)
		}
	case n.elem != nil:
		elems, ok := parseArray(data)
		if !ok {
			return
		}
		for _, e := range elems {
			// mergeArray pairs elements by matchKey, so an element without one
			// never gets its unknown keys carried across. Skipping it here keeps
			// this report to the keys a save would actually preserve.
			if _, named := objectKey(e, matchKey); !named {
				continue
			}
			collectUnknown(e, n.elem, prefix+"[]", seen, out)
		}
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func mergeValue(typed, existing json.RawMessage, n *node) json.RawMessage {
	switch {
	case n == nil:
		return typed
	case n.fields != nil:
		return mergeObject(typed, existing, n.fields)
	case n.elem != nil:
		return mergeArray(typed, existing, n.elem)
	}
	return typed
}

func mergeObject(typed, existing json.RawMessage, fields map[string]*node) json.RawMessage {
	typedMembers, ok := parseObject(typed)
	if !ok {
		return typed
	}
	existingMembers, ok := parseObject(existing)
	if !ok {
		return typed
	}

	byKey := make(map[string]json.RawMessage, len(existingMembers))
	for _, m := range existingMembers {
		byKey[m.key] = m.val
	}

	out := make([]member, 0, len(typedMembers)+len(existingMembers))
	for _, m := range typedMembers {
		if ex, found := byKey[m.key]; found {
			m.val = mergeValue(m.val, ex, fields[m.key])
		}
		out = append(out, m)
	}
	// Keys the type does not model are the user's, not chunk's to rewrite.
	for _, m := range existingMembers {
		if _, modeled := fields[m.key]; !modeled {
			out = append(out, m)
		}
	}
	return encodeObject(out)
}

func mergeArray(typed, existing json.RawMessage, elem *node) json.RawMessage {
	typedElems, ok := parseArray(typed)
	if !ok {
		return typed
	}
	existingElems, ok := parseArray(existing)
	if !ok {
		return typed
	}

	byName := make(map[string]json.RawMessage, len(existingElems))
	for _, e := range existingElems {
		name, named := objectKey(e, matchKey)
		if !named {
			continue
		}
		if _, dup := byName[name]; !dup { // first occurrence wins
			byName[name] = e
		}
	}

	// Iterating typed is what keeps an element that only exists in the file
	// from coming back: there is no branch here that can append one.
	out := make([]json.RawMessage, len(typedElems))
	for i, t := range typedElems {
		out[i] = t
		name, named := objectKey(t, matchKey)
		if !named {
			continue
		}
		if ex, found := byName[name]; found {
			out[i] = mergeValue(t, ex, elem)
		}
	}
	return encodeArray(out)
}

// member is one key/value pair of a JSON object, in document order.
type member struct {
	key string
	val json.RawMessage
}

// parseObject decodes a JSON object into its members in document order. A
// repeated key keeps the position of its first occurrence and the value of its
// last, matching encoding/json. Reports false for anything that is not a
// complete JSON object.
func parseObject(data []byte) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []member
	at := map[string]int{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, isString := keyTok.(string)
		if !isString {
			return nil, false
		}
		// Decoding into a RawMessage consumes exactly one value and keeps its
		// bytes, which is what lets an unknown value survive untouched.
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		if i, dup := at[key]; dup {
			out[i].val = raw
			continue
		}
		at[key] = len(out)
		out = append(out, member{key: key, val: raw})
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return nil, false
	}
	return out, true
}

// parseArray decodes a JSON array into its elements, in order.
func parseArray(data []byte) ([]json.RawMessage, bool) {
	var elems []json.RawMessage
	if err := json.Unmarshal(data, &elems); err != nil {
		return nil, false
	}
	return elems, true
}

// objectKey returns the string value of key in raw, when raw is an object whose
// key holds a string.
func objectKey(raw json.RawMessage, key string) (string, bool) {
	members, ok := parseObject(raw)
	if !ok {
		return "", false
	}
	for _, m := range members {
		if m.key != key {
			continue
		}
		var s string
		if err := json.Unmarshal(m.val, &s); err != nil {
			return "", false
		}
		return s, true
	}
	return "", false
}

func encodeObject(members []member) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(encodeString(m.key))
		buf.WriteByte(':')
		buf.Write(m.val)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func encodeArray(elems []json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, e := range elems {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(e)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

// encodeString quotes s as a JSON string without HTML-escaping, matching how
// chunk writes config files.
func encodeString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return []byte(`""`)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}
