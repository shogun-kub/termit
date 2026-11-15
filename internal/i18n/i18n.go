// Package i18n holds the two languages the node speaks to its owner in: Russian, which is the default,
// and English.
//
// Only the human-facing surface is translated — the menu, the wizard and the page of client links.
// Command output, log lines, error text and everything a script parses stay in English, because
// translating them would make the tool untranslatable in practice: a message with a flag name in it is
// read by an operator who already speaks the command language, and a search engine finds English.
package i18n

import (
	"fmt"
	"strings"
)

// Lang is one of the supported languages.
type Lang string

const (
	// RU is Russian, the default: the project's operators read it, and the guide is written in it.
	RU Lang = "ru"
	// EN is English, the second language and the fallback when a console cannot show Cyrillic.
	EN Lang = "en"
)

// Default is what a node speaks before anybody chooses.
const Default = RU

// Parse accepts the names an operator types and the values stored in the configuration.
func Parse(value string) (Lang, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ru", "russian", "русский", "ru-ru":
		return RU, true
	case "en", "english", "английский", "en-us", "en-gb":
		return EN, true
	default:
		return "", false
	}
}

// String is the value stored in the configuration.
func (l Lang) String() string { return string(l) }

// Valid reports whether l is one the package knows.
func (l Lang) Valid() bool { return l == RU || l == EN }

// Console answers with the language to use on a terminal. Russian is only usable where the environment
// is UTF-8: on a C or latin-1 locale the console renders every Cyrillic byte as a question mark, and a
// menu of question marks is worse than a menu in English. English needs nothing.
//
// getenv is injected so the rule is testable without a real environment.
func Console(selected Lang, getenv func(string) string) Lang {
	if selected != RU {
		return EN
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		value := strings.ToLower(getenv(name))
		if strings.Contains(value, "utf-8") || strings.Contains(value, "utf8") {
			return RU
		}
	}
	return EN
}

// Translator resolves messages for one language.
type Translator struct{ lang Lang }

// New returns a translator. An unknown language behaves as English rather than as an empty screen.
func New(lang Lang) Translator {
	if !lang.Valid() {
		lang = EN
	}
	return Translator{lang: lang}
}

// Lang is the language this translator renders.
func (t Translator) Lang() Lang { return t.lang }

// T returns the message for key, falling back to English and then to the key itself, so a missing
// translation is a visible string rather than a blank line.
func (t Translator) T(key Key) string {
	if value, ok := catalog(t.lang)[key]; ok {
		return value
	}
	if value, ok := catalog(EN)[key]; ok {
		return value
	}
	return string(key)
}

// Tf is T with formatting, for the messages that carry a value.
func (t Translator) Tf(key Key, args ...any) string {
	return fmt.Sprintf(t.T(key), args...)
}

func catalog(lang Lang) map[Key]string {
	if lang == RU {
		return messagesRU
	}
	return messagesEN
}

// Keys returns every message key this build knows. It is what a test walks to prove the two catalogs
// carry the same set.
func Keys() []Key {
	out := make([]Key, 0, len(messagesEN))
	for key := range messagesEN {
		out = append(out, key)
	}
	return out
}
