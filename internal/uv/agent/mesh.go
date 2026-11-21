package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// healthAnswer is the part of /healthz the node reads between scheduled syncs.
type healthAnswer struct {
	Status         string `json:"status"`
	MeshGeneration string `json:"mesh_generation"`
}

// Mesh asks the server which mesh it holds, without a credential and without a peer set. It is the
// cheap read the loop makes when the node is not due to sync: a digest that changed means a node
// joined, left or changed state, and the loop reconciles at once instead of waiting for its schedule.
func (r Remote) Mesh(ctx context.Context) (string, error) {
	if r.BaseURL == "" {
		return "", errors.New("agent: no management server is configured (mn.url)")
	}
	client, err := r.client()
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(r.BaseURL, "/") + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("agent: build the health request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent: read %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent: %s answered %d", endpoint, resp.StatusCode)
	}
	var answer healthAnswer
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return "", fmt.Errorf("agent: decode the health answer: %w", err)
	}
	return answer.MeshGeneration, nil
}
