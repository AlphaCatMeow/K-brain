package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/permissions"
)

func Authorize(ctx context.Context, tool, command string) error {
	if deny := checkGate(ctx, tool, command); deny != "" {
		return errors.New(deny)
	}
	return nil
}

type GateDecision int

const (
	GateAllowOnce GateDecision = iota
	GateAllowAlways
	GateReject
)

type GateRequest struct {
	Context context.Context
	Tool    string
	Command string
	// Shell is the resolved shell that will run Command, for tools that run one;
	// empty when unknown, which permission rules parse strictly.
	Shell       string
	Rule        string
	Description string
}

var Gate func(GateRequest) (GateDecision, string)

type gateContextKey struct{}

type contextGate struct {
	check func(GateRequest) (GateDecision, string)
}

func WithGate(ctx context.Context, gate func(GateRequest) (GateDecision, string)) context.Context {
	return context.WithValue(ctx, gateContextKey{}, contextGate{check: gate})
}

func CommandRule(command string) string {
	cmd := strings.TrimSpace(command)

	for i, r := range cmd {
		if r == '&' || r == '|' || r == ';' || r == '>' || r == '<' {
			cmd = strings.TrimSpace(cmd[:i])
			break
		}
	}
	tokens := strings.Fields(cmd)
	for i := 0; i < len(tokens); i++ {
		if strings.Contains(tokens[i], "=") && !strings.HasPrefix(tokens[i], "-") && i == 0 {

			tokens = tokens[1:]
			i = -1
		}
	}
	return strings.Join(permissions.CommandPrefix(tokens), " ")
}

func checkGate(ctx context.Context, tool, command string) string {
	return checkShellGate(ctx, tool, command, "")
}

// checkShellGate is checkGate for a command that shell will run.
func checkShellGate(ctx context.Context, tool, command, shell string) string {
	var gate func(GateRequest) (GateDecision, string)
	if scoped, ok := ctx.Value(gateContextKey{}).(contextGate); ok {
		gate = scoped.check
	} else {
		gate = Gate
	}
	if gate == nil {
		return ""
	}
	decision, redirect := gate(GateRequest{Context: ctx, Tool: tool, Command: command, Shell: shell, Rule: CommandRule(command)})
	if decision == GateReject {
		if redirect == "" {
			redirect = "the user rejected this action"
		}
		return "Permission denied: " + redirect
	}
	return ""
}
