package secrets

import (
	"errors"
	"testing"
)

func TestIsSecretRef(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"op://vault/item/field", true},
		{"op://", true},
		{"OP://vault/item/field", false}, // case-sensitive
		{"plaintext", false},
		{"", false},
		{"https://example.com", false},
	}
	for _, c := range cases {
		if got := IsSecretRef(c.input); got != c.want {
			t.Errorf("IsSecretRef(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

type fakeResolver struct {
	vals map[string]string
	err  error
}

func (f *fakeResolver) Resolve(ref string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.vals[ref]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func TestResolveAll_NoRefs(t *testing.T) {
	in := map[string]string{"FOO": "bar", "BAZ": "qux"}
	out, err := ResolveAll(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Should be the exact same map (fast path)
	if len(out) != len(in) {
		t.Errorf("expected same length map, got %d", len(out))
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("out[%q] = %q, want %q", k, out[k], v)
		}
	}
}

func TestResolveAll_NilMap(t *testing.T) {
	out, err := ResolveAll(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("expected nil, got %v", out)
	}
}

func TestResolveAll_EmptyMap(t *testing.T) {
	out, err := ResolveAll(map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty map, got %v", out)
	}
}

func TestResolveAll_MixedRefs(t *testing.T) {
	resolver := &fakeResolver{vals: map[string]string{
		"op://vault/item/key": "supersecret",
	}}
	in := map[string]string{
		"PLAIN":  "value",
		"SECRET": "op://vault/item/key",
	}
	out, err := ResolveAll(in, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if out["PLAIN"] != "value" {
		t.Errorf("PLAIN = %q, want %q", out["PLAIN"], "value")
	}
	if out["SECRET"] != "supersecret" {
		t.Errorf("SECRET = %q, want %q", out["SECRET"], "supersecret")
	}
}

func TestResolveAll_ErrorPropagation(t *testing.T) {
	resolver := &fakeResolver{err: errors.New("not signed in")}
	in := map[string]string{"KEY": "op://vault/item/field"}
	_, err := ResolveAll(in, resolver)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Error should mention the key name but not necessarily the vault path
	var re *resolveError
	if !errors.As(err, &re) {
		t.Errorf("expected resolveError, got %T: %v", err, err)
	}
	if re.key != "KEY" {
		t.Errorf("resolveError.key = %q, want %q", re.key, "KEY")
	}
}

func TestResolveAll_NilResolverUsesOpResolver(t *testing.T) {
	// With no refs, nil resolver should be fine (fast path).
	out, err := ResolveAll(map[string]string{"X": "y"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["X"] != "y" {
		t.Errorf("X = %q, want y", out["X"])
	}
}
