package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shogun-kub/termit/internal/proto"
)

// Unblock asks the management server to let this node back into the mesh. It is the only call a
// blocked node can make: every other endpoint treats it as gone until an explicit, successful check
// says otherwise. The request may carry the identity the node answers on now, which is what a
// rebuilt box sends.
func (r Remote) Unblock(ctx context.Context, agentKey string, request proto.UnblockRequest) (proto.UnblockResponse, error) {
	var answer proto.UnblockResponse
	err := r.postAgent(ctx, agentKey, "/api/v1/agent/unblock", "unblock", request, &answer)
	return answer, err
}

// Relocate asks the server to move this node to the domain the box now answers on (Scenario C). It
// carries the same identity an unblock does; the server verifies it against the address that sent the
// request before it touches the record.
func (r Remote) Relocate(ctx context.Context, agentKey string, request proto.RelocateRequest) (proto.RelocateResponse, error) {
	var answer proto.RelocateResponse
	err := r.postAgent(ctx, agentKey, "/api/v1/agent/relocate", "relocate", request, &answer)
	return answer, err
}

// postAgent is the shared shape of the requests a node makes about itself: a JSON body, the agent key
// in the header, and a problem body for anything but 200.
func (r Remote) postAgent(ctx context.Context, agentKey, path, what string, request, response any) error {
	if r.BaseURL == "" {
		return errors.New("agent: no management server is configured (mn.url)")
	}
	if agentKey == "" {
		return errors.New("agent: no agent key; this node has no credentials")
	}
	client, err := r.client()
	if err != nil {
		return err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("agent: encode the %s request: %w", what, err)
	}
	endpoint := strings.TrimRight(r.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent: build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("agent: %s with %s: %w", what, r.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var problem proto.Problem
		if err := json.NewDecoder(resp.Body).Decode(&problem); err == nil && problem.Code != "" {
			return &Refusal{Status: resp.StatusCode, Problem: problem}
		}
		return fmt.Errorf("agent: the server answered %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("agent: decode the %s answer: %w", what, err)
	}
	return nil
}
