package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
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

// Element is one member of a set.
type Element struct {
	Value string
	// Expires is the time left before the kernel forgets it; zero means it stays until removed.
	Expires time.Duration
}

// Elements reads one set. The guard uses it for the scan sets (who the meter remembered) and the
// reconcile step uses it for the ban sets.
func (a Applier) Elements(ctx context.Context, table, set string) ([]Element, error) {
	if err := validateName(table); err != nil {
		return nil, err
	}
	if err := validateName(set); err != nil {
		return nil, err
	}
	if a.Runner == nil {
		return nil, errors.New("firewall: no runner")
	}
	out, err := a.Runner.Run(ctx, a.binary(), []string{"-j", "list", "set", "inet", table, set}, "")
	if err != nil {
		return nil, fmt.Errorf("firewall: read set %s: %w", set, err)
	}
	return parseElements(out)
}

// AddElement puts one address in a set, with a TTL in seconds or none at all for a permanent member.
func (a Applier) AddElement(ctx context.Context, table, set, value string, ttl time.Duration) error {
	if err := validateElement(table, set, value); err != nil {
		return err
	}
	if a.Runner == nil {
		return errors.New("firewall: no runner")
	}
	element := value
	if ttl > 0 {
		element += fmt.Sprintf(" timeout %ds", int64(ttl/time.Second))
	}
	if _, err := a.Runner.Run(ctx, a.binary(),
		[]string{"add", "element", "inet", table, set, "{ " + element + " }"}, ""); err != nil {
		return fmt.Errorf("firewall: add %s to %s: %w", value, set, err)
	}
	return nil
}

// DeleteElement removes one address from a set. A caller that cannot be sure the element is still
// there — anything an operator triggers — uses RemoveBan, which asks first.
func (a Applier) DeleteElement(ctx context.Context, table, set, value string) error {
	if err := validateElement(table, set, value); err != nil {
		return err
	}
	if a.Runner == nil {
		return errors.New("firewall: no runner")
	}
	if _, err := a.Runner.Run(ctx, a.binary(),
		[]string{"delete", "element", "inet", table, set, "{ " + value + " }"}, ""); err != nil {
		return fmt.Errorf("firewall: remove %s from %s: %w", value, set, err)
	}
	return nil
}

// RemoveBan takes one address out of whichever ban set holds it, and does nothing when neither does.
// It asks the set rather than reading nft's error text, so "the kernel already expired it" stays a
// success.
func (a Applier) RemoveBan(ctx context.Context, table, value string) error {
	return a.removeFrom(ctx, table, value, "banned4", "banned6")
}

// ForgetScan takes one address out of the kernel's scan sets. An unban does this too: without it the
// meter's memory, which lives for minutes, would ban the same source again on the next cycle and the
// operator's decision would look broken.
func (a Applier) ForgetScan(ctx context.Context, table, value string) error {
	return a.removeFrom(ctx, table, value, "scan4", "scan6")
}

// removeFrom deletes value from each named set that holds it.
func (a Applier) removeFrom(ctx context.Context, table, value string, sets ...string) error {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("firewall: %q is not an address", value)
	}
	for _, set := range sets {
		elements, err := a.Elements(ctx, table, set)
		if err != nil {
			return err
		}
		for _, element := range elements {
			if element.Value == addr.String() {
				if err := a.DeleteElement(ctx, table, set, addr.String()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateElement keeps the arguments to an IP and a set name: the address is rendered into a command
// line, so anything else is refused rather than handed to nft.
func validateElement(table, set, value string) error {
	if err := validateName(table); err != nil {
		return err
	}
	if err := validateName(set); err != nil {
		return err
	}
	if _, err := netip.ParseAddr(strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("firewall: %q is not an address", value)
	}
	return nil
}

// parseElements reads `nft -j list set`. nft returns two shapes: a bare string for an element without
// a timeout, and an object for one with.
func parseElements(out string) ([]Element, error) {
	objects, err := decodeObjects(out)
	if err != nil {
		return nil, err
	}
	elements := make([]Element, 0)
	for _, object := range objects {
		if object.Set == nil {
			continue
		}
		for _, raw := range object.Set.Elem {
			var asString string
			if err := json.Unmarshal(raw, &asString); err == nil {
				elements = append(elements, Element{Value: asString})
				continue
			}
			var wrapped struct {
				Elem struct {
					Val     string `json:"val"`
					Expires int64  `json:"expires"`
				} `json:"elem"`
			}
			if err := json.Unmarshal(raw, &wrapped); err != nil {
				return nil, fmt.Errorf("firewall: decode set element: %w", err)
			}
			elements = append(elements, Element{
				Value:   wrapped.Elem.Val,
				Expires: time.Duration(wrapped.Elem.Expires) * time.Second,
			})
		}
	}
	return elements, nil
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
