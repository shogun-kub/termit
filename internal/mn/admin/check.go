package admin

import (
	"context"
	"encoding/json"
	"fmt"
)

// checkNow runs one cycle for one node, synchronously, and answers with what happened. It is the
// operator's way to ask "is this node really as broken as the scheduler thinks", and the fastest way to
// see the scheduler's own error text when something is wrong with the server rather than the node.
func (s *Service) checkNow(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args inviteIDArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a node id or domain is required"}
	}
	node, refused := s.resolveUV(ctx, args.ID)
	if refused != nil {
		return nil, refused
	}
	if s.Runner == nil {
		return nil, &Error{Code: CodeUnavailable, Detail: "this server has no scheduler"}
	}
	cycle, err := s.Runner.CheckNow(ctx, node)
	if err != nil {
		// The error is the answer here: it says the server could not complete the cycle, which is
		// different from the node failing.
		return nil, &Error{Code: CodeInternal, Detail: fmt.Sprintf("the cycle failed: %v", err)}
	}
	return map[string]any{
		"node":     view(cycle.Node),
		"event":    string(cycle.Event),
		"state":    string(cycle.Outcome.Next),
		"changed":  cycle.Outcome.Changed,
		"reason":   cycle.Outcome.Reason,
		"attempts": cycle.Attempts,
		"class":    string(cycle.Result.Class),
	}, nil
}
