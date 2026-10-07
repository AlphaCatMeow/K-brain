package agent

import (
	"context"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type RequestObservation struct {
	ToolCallID   string
	TaskID       string
	ParentTaskID string
	Provider     string
	Endpoint     string
	Request      ai.Request
}

type RequestObserver interface {
	Start(context.Context, RequestObservation) (context.Context, error)
	End(context.Context, ai.Message, ai.Usage, error)
	Retry(context.Context, ai.RetryEvent)
}

type requestObserverKey struct{}
type requestTaskKey struct{}
type requestToolKey struct{}
type requestTaskScope struct{ ID, Parent string }

func WithRequestObserver(ctx context.Context, observer RequestObserver) context.Context {
	return context.WithValue(ctx, requestObserverKey{}, observer)
}

func withRequestTask(ctx context.Context, id string) context.Context {
	parent, _ := ctx.Value(requestTaskKey{}).(requestTaskScope)
	return context.WithValue(ctx, requestTaskKey{}, requestTaskScope{ID: id, Parent: parent.ID})
}

func (a *Agent) streamObserved(ctx context.Context, request ai.Request, ev Events) (ai.Message, ai.Usage, error) {
	if a.turnTimeContext {
		request.Messages = withTurnTimes(request.Messages)
	}
	observer, _ := ctx.Value(requestObserverKey{}).(RequestObserver)
	if observer != nil {
		scope, _ := ctx.Value(requestTaskKey{}).(requestTaskScope)
		var err error
		toolCallID, _ := ctx.Value(requestToolKey{}).(string)
		ctx, err = observer.Start(ctx, RequestObservation{ToolCallID: toolCallID, TaskID: scope.ID, ParentTaskID: scope.Parent, Provider: a.Provider, Endpoint: a.Client.Endpoint(), Request: request})
		if err != nil {
			return ai.Message{}, ai.Usage{}, err
		}
		original := ev.OnRetry
		ev.OnRetry = func(event ai.RetryEvent) {
			observer.Retry(ctx, event)
			if original != nil {
				original(event)
			}
		}
	}
	if ev.OnHostedSearch != nil {
		ctx = ai.WithSearchObserver(ctx, ev.OnHostedSearch)
	}
	clearRetry := a.reportRetries(ev)
	defer clearRetry()
	message, usage, err := a.Client.Stream(ctx, request, ev.OnText, ev.OnThink, ev.OnToolCall)
	if observer != nil {
		observer.End(ctx, message, usage, err)
	}
	return message, usage, err
}

// WithTurnTimeContext supplies current context alongside a clock-free system prompt.
func WithTurnTimeContext() Option {
	return func(a *Agent) { a.turnTimeContext = true }
}

// Render persisted timestamps only in requests, without changing displayed history.
// UTC avoids prefix changes when a session reloads in a different local timezone.
func withTurnTimes(messages []ai.Message) []ai.Message {
	out := append([]ai.Message(nil), messages...)
	for i, message := range out {
		if message.Role == "user" && message.Authored && message.SentAt != nil && !message.SentAt.IsZero() {
			out[i].Content = "<kbrain-turn-time>" + message.SentAt.UTC().Format(time.RFC3339) + "</kbrain-turn-time>\n\n" + message.Content
		}
	}
	return out
}
