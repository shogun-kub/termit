// Package termui is the small interactive front end both binaries share: a numbered menu, prompts
// with defaults, confirmations, and colour when the terminal can take it.
//
// It is deliberately line-oriented rather than a full-screen TUI. Two reasons. The model it replaces —
// a numbered menu you answer with a key and Enter — is what people already know from installing
// servers, and it behaves identically over ssh, in a serial console, and in `docker exec -it`. And a
// line-oriented UI needs no terminal mode, no third-party dependency, and can be driven by a script in
// a test, which a raw-mode TUI cannot without faking a terminal.
package termui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Style holds the escape sequences for one colour scheme. The zero Style writes nothing, which is what
// a piped or dumb terminal gets.
type Style struct {
	Reset, Bold, Dim, Red, Green, Yellow, Cyan string
}

// Colour is the scheme used on a terminal: green for good, red for bad, yellow for attention, cyan for
// structure.
func Colour() Style {
	return Style{
		Reset:  "\x1b[0m",
		Bold:   "\x1b[1m",
		Dim:    "\x1b[2m",
		Red:    "\x1b[31m",
		Green:  "\x1b[32m",
		Yellow: "\x1b[33m",
		Cyan:   "\x1b[36m",
	}
}

// UI is one session of the interactive front end.
type UI struct {
	in  io.Reader
	out io.Writer
	sty Style
	// scan is created once and reused: a menu answer, a prompt and the next menu answer all come from
	// the same stream, and recreating it would drop buffered input.
	scan *bufio.Scanner
}

// New returns a UI reading answers from in and drawing on out. Pass an empty Style for a plain,
// uncoloured session.
func New(in io.Reader, out io.Writer, style Style) *UI {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 256), 64*1024)
	return &UI{in: in, out: out, sty: style, scan: scanner}
}

// Colour reports whether this session draws colour.
func (u *UI) Colour() bool { return u.sty.Reset != "" }

// IsTerminal reports whether f is attached to a terminal. It is the standard character-device test,
// which is enough to decide about colour, clear-screen sequences and whether the bare binary should
// open the menu.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Interactive reports whether the default front end should take over: both streams are terminals and
// the environment has not asked for plain output.
func Interactive(in, out *os.File) bool {
	if !IsTerminal(in) || !IsTerminal(out) {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return true // still a terminal; colour is handled separately
	}
	return os.Getenv("TERM") != "dumb"
}

// PlainColour reports the style to use for a terminal session, honouring NO_COLOR and TERM=dumb.
func PlainColour(out *os.File) Style {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return Style{}
	}
	if os.Getenv("TERM") == "dumb" {
		return Style{}
	}
	if !IsTerminal(out) {
		return Style{}
	}
	return Colour()
}

// Clear blanks the screen and puts the cursor at the top. Without colour it does nothing: a log
// capture should read as a sequence of sections, not as one overwritten page.
func (u *UI) Clear() {
	if !u.Colour() {
		return
	}
	fmt.Fprint(u.out, "\x1b[2J\x1b[H")
}

// Title prints the name of the screen.
func (u *UI) Title(text string) {
	fmt.Fprintf(u.out, "%s%s%s\n", u.sty.Bold, text, u.sty.Reset)
	fmt.Fprintln(u.out, strings.Repeat("─", min(72, max(20, len([]rune(text))))))
}

// Hint prints dim explanatory text under the title.
func (u *UI) Hint(text string) {
	fmt.Fprintf(u.out, "%s%s%s\n", u.sty.Dim, text, u.sty.Reset)
}

// KV prints an aligned key/value line.
func (u *UI) KV(key, value string) {
	fmt.Fprintf(u.out, "  %s%-14s%s %s\n", u.sty.Dim, key, u.sty.Reset, value)
}

// State prints a coloured state word and a detail: ok, warn, fail, skip.
func (u *UI) State(status, detail string) {
	colour, mark := "", "•"
	switch status {
	case "ok":
		colour, mark = u.sty.Green, "✔"
	case "warn", "pending":
		colour, mark = u.sty.Yellow, "!"
	case "fail", "blocked":
		colour, mark = u.sty.Red, "✖"
	case "skip":
		colour, mark = u.sty.Dim, "–"
	}
	fmt.Fprintf(u.out, "  %s%s%s %s\n", colour, mark, u.sty.Reset, detail)
}

// Rule prints a horizontal line.
func (u *UI) Rule() {
	fmt.Fprintln(u.out, strings.Repeat("─", 72))
}

// Blank prints an empty line.
func (u *UI) Blank() { fmt.Fprintln(u.out) }

// Item is one menu entry.
type Item struct {
	// Key is what the operator types. Numbers are assigned by Menu, so a caller leaves it empty.
	Key string
	// Label is the entry itself.
	Label string
	// Hint is the one-line explanation to its right; optional.
	Hint string
}

// Menu draws items and returns the key of the chosen one. An empty key means the operator quit.
//
// Invalid input redraws instead of failing: a menu that exits on a typo is a menu nobody trusts.
func (u *UI) Menu(items []Item) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("termui: a menu needs at least one item")
	}
	numbered := make([]Item, len(items))
	copy(numbered, items)
	width := 0
	for i := range numbered {
		if numbered[i].Key == "" {
			numbered[i].Key = fmt.Sprint(i + 1)
		}
		if w := len([]rune(numbered[i].Key)); w > width {
			width = w
		}
	}
	labels := 0
	for _, item := range numbered {
		if w := len([]rune(item.Label)); w > labels {
			labels = w
		}
	}

	u.Blank()
	for _, item := range numbered {
		fmt.Fprintf(u.out, "  %s%s%s%*s  %-*s", u.sty.Cyan, item.Key, u.sty.Reset,
			width-len([]rune(item.Key)), "", labels, item.Label)
		if item.Hint != "" {
			fmt.Fprintf(u.out, "  %s%s%s", u.sty.Dim, item.Hint, u.sty.Reset)
		}
		fmt.Fprintln(u.out)
	}
	fmt.Fprintf(u.out, "  %s%s%s%*s  %s\n", u.sty.Cyan, "q", u.sty.Reset, width-1, "", "quit")

	for {
		fmt.Fprintf(u.out, "\nchoose [%s1-%d, q%s]: ", u.sty.Bold, len(numbered), u.sty.Reset)
		answer, err := u.readLine()
		if err != nil {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		switch answer {
		case "q", "Q", "quit", "exit":
			return "", nil
		case "":
			continue
		}
		for _, item := range numbered {
			if strings.EqualFold(answer, item.Key) {
				return item.Key, nil
			}
		}
		// A label is an acceptable answer too: it is what the operator is looking at.
		for _, item := range numbered {
			if strings.EqualFold(answer, item.Label) {
				return item.Key, nil
			}
		}
		u.State("warn", fmt.Sprintf("%q is not one of the choices", answer))
	}
}

// Prompt asks for a value. An empty answer takes the default, which may itself be empty.
func (u *UI) Prompt(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(u.out, "%s [%s%s%s]: ", label, u.sty.Dim, def, u.sty.Reset)
	} else {
		fmt.Fprintf(u.out, "%s: ", label)
	}
	answer, err := u.readLine()
	if err != nil {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// Confirm asks a yes/no question. An empty answer takes the default.
func (u *UI) Confirm(label string, def bool) (bool, error) {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}
	fmt.Fprintf(u.out, "%s [%s]: ", label, suffix)
	answer, err := u.readLine()
	if err != nil {
		return def, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "":
		return def, nil
	case "y", "yes", "д", "да":
		return true, nil
	case "n", "no", "н", "нет":
		return false, nil
	default:
		return def, nil
	}
}

// Pause waits for Enter, so a result stays on screen until it has been read.
func (u *UI) Pause() error {
	fmt.Fprintf(u.out, "\n%sEnter to continue%s ", u.sty.Dim, u.sty.Reset)
	_, err := u.readLine()
	return err
}

// Say prints a plain line of output.
func (u *UI) Say(text string) { fmt.Fprintln(u.out, text) }

// Error prints a failure in red.
func (u *UI) Error(text string) {
	fmt.Fprintf(u.out, "%s%s%s\n", u.sty.Red, text, u.sty.Reset)
}

// Index turns a menu answer back into a zero-based position. It is a separate function because both
// binaries need exactly this mapping and neither should own it.
func Index(key string) (int, error) {
	if key == "" {
		return 0, fmt.Errorf("termui: empty menu answer")
	}
	index := 0
	for _, r := range key {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("termui: %q is not a menu number", key)
		}
		index = index*10 + int(r-'0')
	}
	if index < 1 {
		return 0, fmt.Errorf("termui: %q is not a menu number", key)
	}
	return index - 1, nil
}

// readLine reads one line of input. End of input before any answer is reported as io.EOF so a caller
// can end the session cleanly instead of looping on an empty stream.
func (u *UI) readLine() (string, error) {
	if u.scan.Scan() {
		return u.scan.Text(), nil
	}
	if err := u.scan.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}
