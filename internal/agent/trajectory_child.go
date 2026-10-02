package agent

import (
	"context"
	"fmt"
)

type childRequestObserver interface {
	ChildStart(context.Context, string, string)
	ChildEnd(context.Context, string, error)
	ChildEvents(string) Events
}

func (a *Agent) observedChildTurn(ctx context.Context, prompt string) (string, error) {
	id := fmt.Sprintf("foreground-%d", taskIDCounter.Add(1))
	ctx = withRequestTask(ctx, id)
	events := Events{}
	observer, _ := ctx.Value(requestObserverKey{}).(childRequestObserver)
	if observer != nil {
		observer.ChildStart(ctx, id, prompt)
		events = observer.ChildEvents(id)
	}
	result, err := a.Turn(ctx, prompt, events)
	if observer != nil {
		observer.ChildEnd(ctx, id, err)
	}
	return result, err
}
