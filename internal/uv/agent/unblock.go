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
	if r.BaseURL == "" {
		return proto.UnblockResponse{}, errors.New("agent: no management server is configured (mn.url)")
	}
	if agentKey == "" {
		return proto.UnblockResponse{}, errors.New("agent: no agent key; this node has no credentials")
	}
	client, err := r.client()
	if err != nil {
		return proto.UnblockResponse{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return proto.UnblockResponse{}, fmt.Errorf("agent: encode the unblock request: %w", err)
	}
	endpoint := strings.TrimRight(r.BaseURL, "/") + "/api/v1/agent/unblock"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return proto.UnblockResponse{}, fmt.Errorf("agent: build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentKey)

	resp, err := client.Do(req)
	if err != nil {
		return proto.UnblockResponse{}, fmt.Errorf("agent: unblock with %s: %w", r.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var problem proto.Problem
		if err := json.NewDecoder(resp.Body).Decode(&problem); err == nil && problem.Code != "" {
			return proto.UnblockResponse{}, &Refusal{Status: resp.StatusCode, Problem: problem}
		}
		return proto.UnblockResponse{}, fmt.Errorf("agent: the server answered %s", resp.Status)
	}
	var answer proto.UnblockResponse
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return proto.UnblockResponse{}, fmt.Errorf("agent: decode the unblock answer: %w", err)
	}
	return answer, nil
}
