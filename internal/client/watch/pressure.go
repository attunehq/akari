package watch

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"

	"github.com/jssblck/akari/internal/client/upload"
)

func pressureFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, upload.ErrRetryableStatus) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func waitForPressureBackoff(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
