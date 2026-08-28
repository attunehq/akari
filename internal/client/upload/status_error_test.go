package upload

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestResponseStatusErrorMarksOnlyRetryableResponses(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: http.StatusBadRequest},
		{status: http.StatusTooManyRequests, want: true},
		{status: http.StatusBadGateway, want: true},
	}
	for _, test := range tests {
		err := newResponseStatusError("POST /ingest", test.status, []byte("upstream error\n"))
		if got := errors.Is(err, ErrRetryableStatus); got != test.want {
			t.Errorf("status %d retryable = %v, want %v", test.status, got, test.want)
		}
		if !strings.Contains(err.Error(), "server returned") || strings.Contains(err.Error(), "\n") {
			t.Errorf("status %d error = %q", test.status, err)
		}
	}
}
