package agent

import "context"

type lifecycleStartKey struct{}

func WithLifecycleStart(ctx context.Context, callback func()) context.Context {
	return context.WithValue(ctx, lifecycleStartKey{}, callback)
}

func lifecycleStart(ctx context.Context) func() {
	if callback, ok := ctx.Value(lifecycleStartKey{}).(func()); ok {
		return callback
	}
	return nil
}

// turnLifecycle pairs round boundaries even on model failure.
type turnLifecycle struct {
	callback func(string)
	round    bool
	message  bool
}

func newTurnLifecycle(callback func(string)) *turnLifecycle {
	return &turnLifecycle{callback: callback}
}

func (l *turnLifecycle) emit(event string) {
	if l.callback != nil {
		l.callback(event)
	}
}

func (l *turnLifecycle) startRound() {
	l.endRound()
	l.round, l.message = true, true
	l.emit("turn_start")
	l.emit("message_start")
}

func (l *turnLifecycle) endMessage() {
	if l.message {
		l.message = false
		l.emit("message_end")
	}
}

func (l *turnLifecycle) endRound() {
	l.endMessage()
	if l.round {
		l.round = false
		l.emit("turn_end")
	}
}

func (l *turnLifecycle) close() {
	l.endRound()
	l.emit("agent_end")
}
