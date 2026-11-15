package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Runner runs one external command with a string on its standard input and returns its standard
// output. It exists so the package can be tested without root, nftables or a kernel.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin string) (stdout string, err error)
}

// Applier applies a rendered table through nft. The zero value is useless: a Runner is required.
type Applier struct {
	Runner Runner
	// Binary overrides the nft command; empty means DefaultBinary.
	Binary string
}

func (a Applier) binary() string {
	if a.Binary != "" {
		return a.Binary
	}
	return DefaultBinary
}

// Apply feeds the ruleset to nft on standard input. nft commits the whole file as one transaction, so
// a table is either fully replaced or untouched: a failure halfway through leaves the previous table
// in force.
func (a Applier) Apply(ctx context.Context, ruleset []byte) error {
	if a.Runner == nil {
		return errors.New("firewall: no runner")
	}
	if _, err := a.Runner.Run(ctx, a.binary(), []string{"-f", "-"}, string(ruleset)); err != nil {
		return fmt.Errorf("firewall: apply the table: %w", err)
	}
	return nil
}

// Check parses the ruleset without applying it. It is what `--dry-run` asks nft, so a dry run catches
// the same syntax errors a real apply would.
func (a Applier) Check(ctx context.Context, ruleset []byte) error {
	if a.Runner == nil {
		return errors.New("firewall: no runner")
	}
	if _, err := a.Runner.Run(ctx, a.binary(), []string{"-c", "-f", "-"}, string(ruleset)); err != nil {
		return fmt.Errorf("firewall: the ruleset does not parse: %w", err)
	}
	return nil
}

// Remove deletes the table and everything in it. `destroy` is the idempotent form of `delete`: it
// succeeds when the table is not there, which is what `firewall off` on a clean host needs.
func (a Applier) Remove(ctx context.Context, table string) error {
	if err := validateName(table); err != nil {
		return err
	}
	if a.Runner == nil {
		return errors.New("firewall: no runner")
	}
	if _, err := a.Runner.Run(ctx, a.binary(), []string{"-f", "-"}, "destroy table inet "+table+"\n"); err != nil {
		return fmt.Errorf("firewall: remove table %s: %w", table, err)
	}
	return nil
}

// Present reports whether the table exists. It asks for the list of tables rather than for the table
// itself: `nft list table` reports an absent table as an error, and telling "absent" apart from
// "failed" would mean reading the error's text.
func (a Applier) Present(ctx context.Context, table string) (bool, error) {
	if a.Runner == nil {
		return false, errors.New("firewall: no runner")
	}
	out, err := a.Runner.Run(ctx, a.binary(), []string{"-j", "list", "tables"}, "")
	if err != nil {
		return false, fmt.Errorf("firewall: list the tables: %w", err)
	}
	names, err := parseTableNames(out)
	if err != nil {
		return false, err
	}
	return names[table], nil
}

// Counts reports how many elements each set of the table holds. A missing table is not an error: it
// means every set is empty.
func (a Applier) Counts(ctx context.Context, table string) (map[string]int, error) {
	if err := validateName(table); err != nil {
		return nil, err
	}
	if a.Runner == nil {
		return nil, errors.New("firewall: no runner")
	}
	present, err := a.Present(ctx, table)
	if err != nil || !present {
		return map[string]int{}, err
	}
	out, err := a.Runner.Run(ctx, a.binary(), []string{"-j", "list", "table", "inet", table}, "")
	if err != nil {
		return nil, fmt.Errorf("firewall: inspect table %s: %w", table, err)
	}
	return parseSetCounts(out)
}

// nftObject is one entry of nft's JSON array. Only the shapes this package reads are declared.
type nftObject struct {
	Table *struct {
		Family string `json:"family"`
		Name   string `json:"name"`
	} `json:"table"`
	Set *struct {
		Name string            `json:"name"`
		Elem []json.RawMessage `json:"elem"`
	} `json:"set"`
}

func decodeObjects(out string) ([]nftObject, error) {
	var document struct {
		Nftables []nftObject `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		return nil, fmt.Errorf("firewall: decode nft output: %w", err)
	}
	return document.Nftables, nil
}

// parseTableNames reads `nft -j list tables`.
func parseTableNames(out string) (map[string]bool, error) {
	objects, err := decodeObjects(out)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(objects))
	for _, object := range objects {
		if object.Table != nil && object.Table.Family == "inet" {
			names[object.Table.Name] = true
		}
	}
	return names, nil
}

// parseSetCounts reads `nft -j list table`.
func parseSetCounts(out string) (map[string]int, error) {
	objects, err := decodeObjects(out)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(objects))
	for _, object := range objects {
		if object.Set != nil {
			counts[object.Set.Name] = len(object.Set.Elem)
		}
	}
	return counts, nil
}

// Describe renders a count map for a status line: "3 bans, 1240 blocked, 12 scans".
func Describe(counts map[string]int) string {
	parts := make([]string, 0, 3)
	add := func(label string, values ...string) {
		total := 0
		for _, name := range values {
			total += counts[name]
		}
		if total > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", total, label))
		}
	}
	add("bans", "banned4", "banned6")
	add("blocked", "blocked4", "blocked6")
	add("scans", "scan4", "scan6")
	if len(parts) == 0 {
		return "no elements"
	}
	return strings.Join(parts, ", ")
}
