package httpcl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Request is an individual HTTP request to be executed by Client.Call.
// Use NewRequest to create one.
type Request struct {
	method  string
	route   string
	url     string
	body    any
	decoder func(io.Reader) error
	headers http.Header
	query   url.Values
}

// URL returns the resolved URL (after RouteParams) or the raw route.
func (r Request) URL() string {
	if r.url != "" {
		return r.url
	}
	return r.route
}

// NewRequest creates a request with functional options.
func NewRequest(method, route string, opts ...func(*Request)) Request {
	r := Request{
		method:  method,
		route:   route,
		headers: http.Header{},
		query:   url.Values{},
	}
	for _, opt := range opts {
		opt(&r)
	}
	return r
}

// Body sets a value that will be JSON-encoded as the request body.
func Body(v any) func(*Request) {
	return func(r *Request) { r.body = v }
}

// JSONDecoder decodes a 2xx response body as JSON into v.
func JSONDecoder(v any) func(*Request) {
	return func(r *Request) {
		r.decoder = func(rd io.Reader) error {
			return json.NewDecoder(rd).Decode(v)
		}
	}
}

// Header sets a single request header.
func Header(key, val string) func(*Request) {
	return func(r *Request) { r.headers.Set(key, val) }
}

// QueryParam sets a single query parameter.
func QueryParam(key, val string) func(*Request) {
	return func(r *Request) { r.query.Set(key, val) }
}

// RouteParams fills route template placeholders (%s, %d, etc.) with the given values.
func RouteParams(params ...any) func(*Request) {
	return func(r *Request) {
		r.url = fmt.Sprintf(r.route, params...)
	}
}
