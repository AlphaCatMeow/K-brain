package browser

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionQueueRespectsCancellation(t *testing.T) {
	s, err := NewManager(ModeHeadless).Session("queued")
	if err != nil {
		t.Fatal(err)
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.Do(ctx, func(Backend) (string, error) { t.Error("cancelled call executed"); return "", nil })
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled call remained blocked behind another browser call")
	}
}
