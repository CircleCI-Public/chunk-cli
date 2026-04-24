package httpcl

import (
	"errors"
	"fmt"
	"net/http"
)

// HTTPError represents a non-2xx HTTP response.
type HTTPError struct {
	Method     string
	Route      string
	StatusCode int
	Body       []byte // raw response body
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.Route, e.StatusCode, http.StatusText(e.StatusCode))
}

// StatusError is a structured HTTP error for use by API client packages. It
// records the operation name and HTTP status code without exposing HTTPError.
type StatusError struct {
	Op         string
	StatusCode int
}

func (e *StatusError) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %d %s", e.Op, e.StatusCode, http.StatusText(e.StatusCode))
	}
	return fmt.Sprintf("%d %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// HasStatusCode checks if err is an *HTTPError with any of the given status codes.
func HasStatusCode(err error, codes ...int) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	for _, c := range codes {
		if he.StatusCode == c {
			return true
		}
	}
	return false
}
