package keyring_test

import (
	"os/user"
	"strings"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/keyring"
	"github.com/smeltdev/smelt/internal/keyring/keyringtest"
)

const svc = "smelt:test:large"

func secretOf(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(strings.Repeat("x", i%7))
	}
	return b.String()[:n]
}

func TestFakeRejectsASingleSetOverTheLimit(t *testing.T) {
	keyringtest.Install(t)
	err := keyring.Set(svc, secretOf(6*1024))
	assert.ErrorIs(t, err, gokeyring.ErrSetDataTooBig)
	assert.Error(t, err, "data passed to Set was too big")
}

func TestSetLargeRoundTrip(t *testing.T) {
	for _, n := range []int{1024, keyring.MaxPartSize, 6 * 1024, 20 * 1024} {
		fake := keyringtest.Install(t)
		want := secretOf(n)
		assert.NilError(t, keyring.SetLarge(svc, want))
		got, err := keyring.GetLarge(svc)
		assert.NilError(t, err)
		assert.Equal(t, got, want)
		if n <= keyring.MaxPartSize {
			assert.DeepEqual(t, fake.Services(), []string{svc})
		}
	}
}

func TestSetLargeKeepsMultibyteTextWhole(t *testing.T) {
	keyringtest.Install(t)
	want := strings.Repeat("é€", 3000)
	assert.NilError(t, keyring.SetLarge(svc, want))
	got, err := keyring.GetLarge(svc)
	assert.NilError(t, err)
	assert.Equal(t, got, want)
}

func TestSetLargeSmallValueIsReadableByGet(t *testing.T) {
	keyringtest.Install(t)
	assert.NilError(t, keyring.SetLarge(svc, "short"))
	got, err := keyring.Get(svc)
	assert.NilError(t, err)
	assert.Equal(t, got, "short")
}

func TestSetLargeOverwriteLeavesNoStaleParts(t *testing.T) {
	fake := keyringtest.Install(t)
	assert.NilError(t, keyring.SetLarge(svc, secretOf(6*1024)))
	assert.Equal(t, len(fake.Services()), 4)

	assert.NilError(t, keyring.SetLarge(svc, secretOf(3*1024)))
	assert.Equal(t, len(fake.Services()), 3)
	got, err := keyring.GetLarge(svc)
	assert.NilError(t, err)
	assert.Equal(t, got, secretOf(3*1024))

	assert.NilError(t, keyring.SetLarge(svc, "small"))
	assert.DeepEqual(t, fake.Services(), []string{svc})
}

func TestGetLargeReportsIncompleteCredentials(t *testing.T) {
	cases := map[string]func(f *keyringtest.Fake){
		"missing part": func(f *keyringtest.Fake) { _ = f.Delete(svc+"#2", username(t)) },
		"altered part": func(f *keyringtest.Fake) { _ = f.Set(svc+"#2", username(t), "tampered") },
		"unknown version": func(f *keyringtest.Fake) {
			_ = f.Set(svc, username(t), "smelt-chunks:v2:3:abc")
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			fake := keyringtest.Install(t)
			assert.NilError(t, keyring.SetLarge(svc, secretOf(6*1024)))
			damage(fake)
			_, err := keyring.GetLarge(svc)
			assert.ErrorIs(t, err, keyring.ErrIncomplete)
			assert.ErrorContains(t, err, "log in again")
		})
	}
}

func TestGetLargeNotFound(t *testing.T) {
	keyringtest.Install(t)
	_, err := keyring.GetLarge(svc)
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestDeleteLargeRemovesEverything(t *testing.T) {
	fake := keyringtest.Install(t)
	assert.NilError(t, keyring.SetLarge(svc, secretOf(20*1024)))
	assert.NilError(t, keyring.DeleteLarge(svc))
	assert.Equal(t, len(fake.Services()), 0)
	assert.NilError(t, keyring.DeleteLarge(svc))
}

// username is the account smelt stores under (brand.KeychainFallbackUser when
// the current user is unknown).
func username(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		return brand.KeychainFallbackUser
	}
	return u.Username
}

func TestSetLargeWritesTheManifestAfterTheParts(t *testing.T) {
	fake := keyringtest.Install(t)
	assert.NilError(t, keyring.SetLarge(svc, secretOf(6*1024)))
	assert.DeepEqual(t, fake.Sets(), []string{svc + "#1", svc + "#2", svc + "#3", svc})
}
