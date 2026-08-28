package upload

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrRetryableStatus marks a response that should pause a long-running client
// before it sends more work to an unavailable or overloaded server.
var ErrRetryableStatus = errors.New("upload server returned a retryable status")

type responseStatusError struct {
	operation string
	status    int
	detail    string
}

func newResponseStatusError(operation string, status int, payload []byte) error {
	return &responseStatusError{operation: operation, status: status, detail: strings.TrimSpace(string(payload))}
}

func (e *responseStatusError) Error() string {
	return fmt.Sprintf("%s: server returned %d: %s", e.operation, e.status, e.detail)
}

func (e *responseStatusError) Is(target error) bool {
	return target == ErrRetryableStatus && (e.status == http.StatusTooManyRequests || e.status >= 500)
}
