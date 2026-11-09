package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// policyKeys are the settings an operator may change at runtime. Everything else lives in the
// configuration file and needs a restart, on purpose: this list is the set of knobs that are worth
// turning while watching a mesh.
var policyKeys = map[string]string{
	"check_interval": "how often a node is checked, as a Go duration (24h, 90m)",
	"probe_retries":  "how many attempts one cycle makes (1–10)",
}

// showPolicy answers with the current runtime overrides and what they mean.
func (s *Service) showPolicy(ctx context.Context) (any, *Error) {
	values := map[string]string{}
	for key := range policyKeys {
		value, err := s.Store.GetSetting(ctx, "policy."+key)
		if err != nil {
			return nil, &Error{Code: CodeInternal, Detail: err.Error()}
		}
		values[key] = value
	}
	return map[string]any{"policy": values, "keys": policyKeys}, nil
}

// setPolicy validates one knob and stores it. The scheduler picks it up on its next pass.
func (s *Service) setPolicy(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || args.Key == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a key and a value are required"}
	}
	help, known := policyKeys[args.Key]
	if !known {
		return nil, &Error{Code: CodeBadRequest, Detail: fmt.Sprintf("unknown policy key %q", args.Key)}
	}
	if args.Value == "" {
		// An empty value removes the override and the configured value applies again.
		if err := s.Store.SetSetting(ctx, "policy."+args.Key, ""); err != nil {
			return nil, &Error{Code: CodeInternal, Detail: err.Error()}
		}
		return map[string]any{"key": args.Key, "value": "", "cleared": true}, nil
	}

	switch args.Key {
	case "check_interval":
		interval, err := time.ParseDuration(args.Value)
		if err != nil {
			return nil, &Error{Code: CodeBadRequest, Detail: help}
		}
		if interval < time.Minute || interval > 30*24*time.Hour {
			return nil, &Error{Code: CodeBadRequest, Detail: "the interval must be between 1m and 720h"}
		}
	case "probe_retries":
		retries, err := strconv.Atoi(args.Value)
		if err != nil || retries < 1 || retries > 10 {
			return nil, &Error{Code: CodeBadRequest, Detail: help}
		}
	}
	if err := s.Store.SetSetting(ctx, "policy."+args.Key, args.Value); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	if err := s.Store.AppendAudit(ctx, store.AuditRecord{At: s.Now().UTC(), Actor: "admin",
		Action: "policy.set", Target: args.Key, DetailJSON: fmt.Sprintf(`{"value":%q}`, args.Value)}); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	return map[string]any{"key": args.Key, "value": args.Value, "cleared": false}, nil
}
