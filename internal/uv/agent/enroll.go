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

// EnrollRequest is what a node needs to register itself.
type EnrollRequest struct {
	// InviteCode is the code the owner was given. Empty is only valid on an open server.
	InviteCode string
	// Domain and BasicVLESSURL describe the node as it is now: the URL is the node's own credential,
	// and the server checks that it names the same host.
	Domain        string
	BasicVLESSURL string
	// AgentVersion is reported to the server and shown in `termit-mn uv list`.
	AgentVersion string
}

// Enroll registers this node and returns the credentials the server issued.
func (r Remote) Enroll(ctx context.Context, req EnrollRequest) (proto.EnrollResponse, error) {
	if r.BaseURL == "" {
		return proto.EnrollResponse{}, errors.New("agent: no management server is configured (mn.url)")
	}
	if req.Domain == "" || req.BasicVLESSURL == "" {
		return proto.EnrollResponse{}, errors.New("agent: a domain and the node's own VLESS URL are required")
	}
	client, err := r.client()
	if err != nil {
		return proto.EnrollResponse{}, err
	}

	body, err := json.Marshal(proto.EnrollRequest{
		InviteCode:    strings.TrimSpace(req.InviteCode),
		Domain:        req.Domain,
		BasicVLESSURL: req.BasicVLESSURL,
		AgentVersion:  req.AgentVersion,
		Profile:       proto.ProfileV1,
	})
	if err != nil {
		return proto.EnrollResponse{}, fmt.Errorf("agent: encode the enrollment: %w", err)
	}
	endpoint := strings.TrimRight(r.BaseURL, "/") + "/api/v1/enroll"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return proto.EnrollResponse{}, fmt.Errorf("agent: build the request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", req.AgentVersion)

	resp, err := client.Do(httpReq)
	if err != nil {
		return proto.EnrollResponse{}, fmt.Errorf("agent: enroll with %s: %w", r.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
	default:
		var problem proto.Problem
		if err := json.NewDecoder(resp.Body).Decode(&problem); err == nil && problem.Code != "" {
			return proto.EnrollResponse{}, &Refusal{Status: resp.StatusCode, Problem: problem}
		}
		return proto.EnrollResponse{}, fmt.Errorf("agent: the server answered %s", resp.Status)
	}

	var answer proto.EnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return proto.EnrollResponse{}, fmt.Errorf("agent: decode the enrollment: %w", err)
	}
	if answer.AgentKey == "" || answer.Node.ID == "" {
		return proto.EnrollResponse{}, errors.New("agent: the enrollment answer carries no credentials")
	}
	return answer, nil
}

// Refusal is a rejection with the server's problem code, so the CLI can say which check failed instead
// of "the server said no".
type Refusal struct {
	Status  int
	Problem proto.Problem
}

func (r *Refusal) Error() string {
	if r.Problem.Detail == "" {
		return r.Problem.Code
	}
	return r.Problem.Code + ": " + r.Problem.Detail
}
