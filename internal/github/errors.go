package github

import (
	"errors"
	"fmt"
)

// ErrTokenNotFound indicates no GitHub token was found in env or config.
var ErrTokenNotFound = errors.New("api token not found")

// RetryError indicates that a GitHub API request failed after exhausting retries.
type RetryError struct {
	Retries     int
	ServerError bool // true when GitHub returned HTML error pages (500/503)
	Err         error
}

func (e *RetryError) Error() string {
	if e.ServerError {
		return fmt.Sprintf("api server error after %d retries", e.Retries)
	}
	if e.Err != nil {
		return fmt.Sprintf("api request failed after %d retries: %v", e.Retries, e.Err)
	}
	return fmt.Sprintf("api request failed after %d retries", e.Retries)
}

func (e *RetryError) Unwrap() error { return e.Err }
