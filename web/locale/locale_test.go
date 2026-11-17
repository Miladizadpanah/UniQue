package locale

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/language"
)

// buildBundle loads en_US + fa_IR straight off disk into a fresh bundle so the
// test doesn't depend on the web package's embedded FS.
func buildBundle(t *testing.T) *i18n.Bundle {
	t.Helper()
	b := i18n.NewBundle(language.MustParse("en-US"))
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	for _, f := range []string{"translate.en_US.toml", "translate.fa_IR.toml"} {
		data, err := os.ReadFile("../translation/" + f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := b.ParseMessageFileBytes(data, "translation/"+f); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
	}
	return b
}

// flattenMessages turns a parsed TOML document into dotted message IDs, the same
// shape go-i18n uses ("pages.core.title").
func flattenMessages(prefix string, in map[string]any, out map[string]string) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch t := v.(type) {
		case map[string]any:
			flattenMessages(key, t, out)
		case string:
			out[key] = t
		}
	}
}

// loadMessages reads one translation file and returns its message IDs.
func loadMessages(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]string{}
	flattenMessages("", doc, out)
	return out
}

// TestEnglishFallback proves the keystone fix: a non-English (Farsi) context asked
// for an en_US-only key returns the English text — not a blank string, and not the
// raw key.
//
// The set of en-only keys is derived from the translation files instead of being
// hard-coded: when fa_IR catches up and translates every key, the test skips rather
// than failing on a stale fixture (which is exactly how the old
// "pages.core.title" version broke).
func TestEnglishFallback(t *testing.T) {
	b := buildBundle(t)
	i18nBundle = b
	localizerDefault = i18n.NewLocalizer(b, "en-US")
	LocalizerWeb = i18n.NewLocalizer(b, "fa-IR") // active locale may lack en-only keys

	en := loadMessages(t, "../translation/translate.en_US.toml")
	fa := loadMessages(t, "../translation/translate.fa_IR.toml")

	var enOnly []string
	for key, text := range en {
		if strings.TrimSpace(text) == "" {
			continue // empty en_US value is not a usable expectation
		}
		if strings.Contains(text, "{{") {
			continue // placeholders need TemplateData; fallback is not the thing under test
		}
		if _, translated := fa[key]; !translated {
			enOnly = append(enOnly, key)
		}
	}
	sort.Strings(enOnly)
	if len(enOnly) == 0 {
		t.Skip("fa_IR translates every en_US key; nothing to exercise the fallback with")
	}

	for _, key := range enOnly {
		want, err := localizerDefault.Localize(&i18n.LocalizeConfig{MessageID: key})
		if err != nil {
			t.Fatalf("en_US localize %q: %v", key, err)
		}

		got := I18n(Web, key)
		if got == "" {
			t.Fatalf("fallback failed for %q: got a blank string", key)
		}
		if got == key {
			t.Fatalf("fallback failed for %q: returned the raw key, not English text", key)
		}
		if got != want {
			t.Fatalf("fallback mismatch for %q: got %q, want English %q", key, got, want)
		}
	}
	t.Logf("fa-IR context: %d en-only key(s) fell back to English", len(enOnly))
}
