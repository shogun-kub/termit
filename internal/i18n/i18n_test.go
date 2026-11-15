package i18n_test

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/i18n"
)

func TestParseAcceptsWhatPeopleType(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"ru", "RU", " ru ", "russian", "русский"} {
		if lang, ok := i18n.Parse(value); !ok || lang != i18n.RU {
			t.Errorf("Parse(%q) = %q, %v", value, lang, ok)
		}
	}
	for _, value := range []string{"en", "EN", "english", "en-US"} {
		if lang, ok := i18n.Parse(value); !ok || lang != i18n.EN {
			t.Errorf("Parse(%q) = %q, %v", value, lang, ok)
		}
	}
	for _, value := range []string{"", "de", "ru-RU-extra", "0"} {
		if _, ok := i18n.Parse(value); ok {
			t.Errorf("Parse(%q) accepted an unknown language", value)
		}
	}
}

// Russian needs a UTF-8 console: on a C locale every Cyrillic byte would come out as a question mark,
// and a menu of question marks is worse than a menu in English.
func TestConsoleFallsBackToEnglish(t *testing.T) {
	t.Parallel()
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	cases := []struct {
		name     string
		env      map[string]string
		selected i18n.Lang
		want     i18n.Lang
	}{
		{name: "utf-8 locale", env: map[string]string{"LANG": "ru_RU.UTF-8"}, selected: i18n.RU, want: i18n.RU},
		{name: "utf8 without the dash", env: map[string]string{"LC_ALL": "C.utf8"}, selected: i18n.RU, want: i18n.RU},
		{name: "c locale", env: map[string]string{"LANG": "C"}, selected: i18n.RU, want: i18n.EN},
		{name: "no locale at all", env: map[string]string{}, selected: i18n.RU, want: i18n.EN},
		{name: "latin-1", env: map[string]string{"LANG": "ru_RU.KOI8-R"}, selected: i18n.RU, want: i18n.EN},
		{name: "english needs nothing", env: map[string]string{}, selected: i18n.EN, want: i18n.EN},
		{name: "LC_ALL wins by being first", env: map[string]string{"LC_ALL": "C", "LANG": "ru_RU.UTF-8"}, selected: i18n.RU, want: i18n.RU},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := i18n.Console(tc.selected, env(tc.env)); got != tc.want {
				t.Errorf("Console(%q) = %q, want %q", tc.selected, got, tc.want)
			}
		})
	}
}

// Every key must exist in both languages: a key in one and not the other is a menu that half the
// operators see as a raw identifier.
func TestBothCatalogsCarryEveryKey(t *testing.T) {
	t.Parallel()
	for _, key := range i18n.Keys() {
		ru := i18n.New(i18n.RU).T(key)
		en := i18n.New(i18n.EN).T(key)
		if ru == string(key) {
			t.Errorf("%s has no Russian message", key)
		}
		if en == string(key) {
			t.Errorf("%s has no English message", key)
		}
	}
}

// A known key must not fall back silently to the other language either.
func TestLanguagesDiffer(t *testing.T) {
	t.Parallel()
	same := 0
	for _, key := range i18n.Keys() {
		ru := i18n.New(i18n.RU).T(key)
		en := i18n.New(i18n.EN).T(key)
		if ru == en {
			same++
		}
	}
	if same > len(i18n.Keys())/4 {
		t.Errorf("%d of %d messages are identical in both languages; the catalogs are probably not translated", same, len(i18n.Keys()))
	}
}

func TestUnknownLangRendersEnglish(t *testing.T) {
	t.Parallel()
	tr := i18n.New(i18n.Lang("de"))
	if tr.Lang() != i18n.EN {
		t.Errorf("an unknown language resolved to %q", tr.Lang())
	}
	if got := tr.T(i18n.MenuTitle); got == string(i18n.MenuTitle) {
		t.Errorf("an unknown language produced no text at all")
	}
}

func TestFormattingKeepsTheVerbs(t *testing.T) {
	t.Parallel()
	for _, lang := range []i18n.Lang{i18n.RU, i18n.EN} {
		got := i18n.New(lang).Tf(i18n.MenuPastEnd, "9")
		if !strings.Contains(got, "9") {
			t.Errorf("%s: the value did not reach the message: %q", lang, got)
		}
	}
}
