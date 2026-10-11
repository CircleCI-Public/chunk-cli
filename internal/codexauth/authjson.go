// Package codexauth holds smelt's side of a Codex login: the auth.json a
// ChatGPT sign-in writes, the copy of it that may enter a container, its
// expiry, and the keychain entries it is stored in.
package codexauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RenewWithin is how long before the access token expires that a login is
// renewed rather than used: a run must not outlive its credential.
const RenewWithin = 24 * time.Hour

// ContainerCopy is auth.json as a container may have it: the refresh token is
// blanked, so a leaked sandbox holds a credential that dies with the access
// token. The field must stay, as an empty string; codex rejects the file
// without it. Every other field is kept as it was.
func ContainerCopy(raw []byte) ([]byte, error) {
	doc, tokens, err := parse(raw)
	if err != nil {
		return nil, err
	}
	blank, _ := json.Marshal("")
	tokens["refresh_token"] = blank
	if doc["tokens"], err = json.Marshal(tokens); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

// AccessToken is the access token in auth.json.
func AccessToken(raw []byte) (string, error) {
	_, tokens, err := parse(raw)
	if err != nil {
		return "", err
	}
	var tok string
	if err := json.Unmarshal(tokens["access_token"], &tok); err != nil || tok == "" {
		return "", errors.New("auth.json has no access token")
	}
	return tok, nil
}

func parse(raw []byte) (doc, tokens map[string]json.RawMessage, err error) {
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("read auth.json: %w", err)
	}
	if err := json.Unmarshal(doc["tokens"], &tokens); err != nil || tokens == nil {
		return nil, nil, errors.New("auth.json has no tokens: it is not from a ChatGPT login")
	}
	return doc, tokens, nil
}

// Expiry is when the access token in auth.json stops working, from its JWT exp
// claim. The token itself is never returned or printed.
func Expiry(raw []byte) (time.Time, error) {
	tok, err := AccessToken(raw)
	if err != nil {
		return time.Time{}, err
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("the access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, fmt.Errorf("read the access token: %w", err)
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&claims); err != nil || claims.Exp == 0 {
		return time.Time{}, errors.New("the access token has no expiry")
	}
	return time.Unix(int64(claims.Exp), 0), nil
}

// State is how much life a login has left.
type State int

// The states a login can be in.
const (
	Fresh   State = iota // more than RenewWithin left
	Soon                 // under RenewWithin left
	Expired              // none left
)

// Classify decides what to do about a login that expires at exp.
func Classify(exp, now time.Time) State {
	switch left := exp.Sub(now); {
	case left <= 0:
		return Expired
	case left < RenewWithin:
		return Soon
	}
	return Fresh
}
