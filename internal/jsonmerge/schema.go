package jsonmerge

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
)

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// node describes what one level of a Go type models: the JSON keys it declares
// and the schema of each key's own type. A nil node means "stop here, the typed
// value wins" — the type either has no keys at all (scalars) or every key it
// could produce is its own (maps), so there is nothing at that level a merge
// could call unknown.
type node struct {
	fields map[string]*node // by JSON object key
	elem   *node            // element schema for slices and arrays
}

// schemaOf derives the node for t, following pointers, slices, and arrays.
func schemaOf(t reflect.Type) *node {
	if t == nil {
		return nil
	}
	// A type that marshals itself decides its own JSON shape, so its Go fields
	// are not object keys and must not be walked as if they were.
	if t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) ||
		reflect.PointerTo(t).Implements(jsonMarshalerType) ||
		reflect.PointerTo(t).Implements(textMarshalerType) {
		return nil
	}

	kind := t.Kind()
	if kind == reflect.Pointer {
		return schemaOf(t.Elem())
	}
	if kind == reflect.Slice || kind == reflect.Array {
		elem := schemaOf(t.Elem())
		if elem == nil {
			return nil
		}
		return &node{elem: elem}
	}
	if kind == reflect.Struct {
		return structNode(t)
	}
	// Scalars declare no keys, every key of a map is already the value's own, and
	// an interface's concrete type is only known at run time. None of them have a
	// level at which a merge could find an unknown key.
	return nil
}

func structNode(t reflect.Type) *node {
	fields := make(map[string]*node, t.NumField())
	var embedded []reflect.StructField

	for i := range t.NumField() {
		f := t.Field(i)
		if !serialized(f) {
			continue
		}
		name, tagged := jsonName(f)
		if name == "" { // json:"-"
			continue
		}
		// An embedded struct with no name of its own has its keys inlined into
		// the parent object, so its schema belongs at this level. Collected for
		// a second pass because a field declared here outranks an inlined one.
		if f.Anonymous && !tagged && deref(f.Type).Kind() == reflect.Struct {
			embedded = append(embedded, f)
			continue
		}
		fields[name] = schemaOf(f.Type)
	}

	for _, f := range embedded {
		inner := schemaOf(f.Type)
		if inner == nil {
			continue
		}
		for key, child := range inner.fields {
			if _, shadowed := fields[key]; !shadowed {
				fields[key] = child
			}
		}
	}
	return &node{fields: fields}
}

// serialized reports whether encoding/json includes f in an object. An embedded
// struct is included even when its type is unexported, because its exported
// fields still are.
func serialized(f reflect.StructField) bool {
	if f.Anonymous {
		return f.IsExported() || deref(f.Type).Kind() == reflect.Struct
	}
	return f.IsExported()
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// jsonName returns the JSON object key encoding/json uses for f, and whether
// the name came from a json tag. The name is empty when the field is skipped.
func jsonName(f reflect.StructField) (name string, tagged bool) {
	tag, ok := f.Tag.Lookup("json")
	if tag == "-" {
		return "", false
	}
	name, _, _ = strings.Cut(tag, ",")
	if name == "" {
		return f.Name, false
	}
	return name, ok
}
