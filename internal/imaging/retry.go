package imaging

import (
	"context"
	"errors"
	"time"
)

func (s *Service) backoff(failures int, hint time.Duration) time.Duration {
	d := s.options.PollInterval
	for i := 1; i < failures && d < s.options.MaxPollInterval; i++ {
		d *= 2
	}
	if d > s.options.MaxPollInterval {
		d = s.options.MaxPollInterval
	}
	if hint > d {
		d = hint
	}
	return d
}
func safeEngineError(err error) *ImageError {
	var ie *ImageError
	if errors.As(err, &ie) {
		return PublicError(ie)
	}
	var ae *AdapterError
	if errors.As(err, &ae) {
		return PublicError(&ImageError{Code: ae.Code})
	}
	if errors.Is(err, ErrUnsupported) {
		return PublicError(&ImageError{Code: CodeUnsupportedOperation})
	}
	return PublicError(&ImageError{Code: CodeUnavailable})
}

// Timers use the injected clock so deadlines, backoff and lease tests need no wall clock waits.
func (s *Service) timedContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	timer := s.clock.After(d)
	go func() {
		select {
		case <-ctx.Done():
		case <-timer:
			cancel()
		}
	}()
	return ctx, cancel
}
func (s *Service) waitRetry(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.clock.After(d):
		return nil
	}
}
