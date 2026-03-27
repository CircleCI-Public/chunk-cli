// Package closer contains a helper for not losing deferred close errors.
package closer

import "io"

// ErrorHandler closes c and, if the incoming error pointed to by in
// is nil, replaces it with the close error (if any).
func ErrorHandler(c io.Closer, in *error) {
	cerr := c.Close()
	if *in == nil {
		*in = cerr
	}
}
