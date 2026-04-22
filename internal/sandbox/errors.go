package sandbox

import (
	"errors"
	"fmt"
)

var (
	// ErrMutuallyExclusiveKeys indicates both a key string and key file were provided.
	ErrMutuallyExclusiveKeys = errors.New("public key and public key file are mutually exclusive")
	// ErrPublicKeyRequired indicates neither a key string nor key file was provided.
	ErrPublicKeyRequired = errors.New("public key is required")
	// ErrPrivateKeyProvided indicates a private key was given where a public key was expected.
	ErrPrivateKeyProvided = errors.New("provided key is a private key")
	// ErrAuthSockNotSet indicates the SSH agent socket is not configured.
	ErrAuthSockNotSet = errors.New("ssh auth socket not set")
)

// KeyNotFoundError indicates the SSH private key file does not exist.
type KeyNotFoundError struct {
	Path string
}

func (e *KeyNotFoundError) Error() string {
	return fmt.Sprintf("ssh key not found: %s", e.Path)
}

// PublicKeyNotFoundError indicates the SSH public key file does not exist.
type PublicKeyNotFoundError struct {
	KeyPath      string
	IdentityFile string
}

func (e *PublicKeyNotFoundError) Error() string {
	return fmt.Sprintf("ssh public key not found: %s", e.KeyPath)
}

// RemoteBaseError indicates the remote merge base could not be resolved.
type RemoteBaseError struct {
	Err error
}

func (e *RemoteBaseError) Error() string {
	return fmt.Sprintf("could not resolve remote base: %v", e.Err)
}

func (e *RemoteBaseError) Unwrap() error { return e.Err }
