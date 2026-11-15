package firewall

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration written in a configuration file the way a person writes it ("10m",
// "24h") instead of as nanoseconds. It lives here because both configurations spell the security
// block the same way, and because a duration that nftables cannot express must be refused where it is
// read, not where the table is rendered.
type Duration time.Duration

// Std returns the standard library value.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String renders the duration the way the configuration writes it: the largest whole unit Go can parse
// back, so "15m" rather than "15m0s". It deliberately does not use nftables' spellings ("1d", "1w"):
// those are right for a ruleset and wrong for a file the loader has to read again.
func (d Duration) String() string {
	value := time.Duration(d)
	if value == 0 {
		return "0s"
	}
	if value < 0 {
		return value.String()
	}
	for _, unit := range []struct {
		size time.Duration
		name string
	}{
		{time.Hour, "h"},
		{time.Minute, "m"},
		{time.Second, "s"},
	} {
		if value%unit.size == 0 {
			return fmt.Sprintf("%d%s", value/unit.size, unit.name)
		}
	}
	return value.String()
}

// UnmarshalYAML reads a duration from a string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		return fmt.Errorf("a duration is written as a string like 10m: %w", err)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("%q is not a duration like 10m or 24h: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML writes the duration back as a string, so a saved configuration stays readable.
func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}
