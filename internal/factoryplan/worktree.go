package factoryplan

import (
	"errors"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// isNotFound is whether err is factory answering 404.
func isNotFound(err error) bool {
	var se *circleci.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}
