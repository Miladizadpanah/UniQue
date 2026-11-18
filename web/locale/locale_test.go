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
// Two fixtures, on purpose:
//
//   - A synthetic message seeded into en_US only. It cannot be skipped, because the
//     failure mode this guards against does not wait for a translation gap to
//     reappear: fa_IR is now complete, so a file-derived fixture would be empty and
//     the test would go quiet exactly when someone later stops relying on it.
//   - The real en-only keys, derived from the two TOML files rather than being
//     hard-coded (the old "pages.core.title" version broke the moment fa_IR learned
//     that word). Today that set is empty; the loop simply has nothing to add.
func TestEnglishFallback(t *testing.T) {
	b := buildBundle(t)

	const synthetic = "test.englishFallbackOnly"
	const syntheticText = "English only"
	if err := b.AddMessages(language.MustParse("en-US"), &i18n.Message{
		ID: synthetic, Other: syntheticText,
	}); err != nil {
		t.Fatalf("seed synthetic en_US-only message: %v", err)
	}

	i18nBundle = b
	localizerDefault = i18n.NewLocalizer(b, "en-US")
	LocalizerWeb = i18n.NewLocalizer(b, "fa-IR") // active locale may lack en-only keys

	en := loadMessages(t, "../translation/translate.en_US.toml")
	fa := loadMessages(t, "../translation/translate.fa_IR.toml")

	keys := []string{synthetic}
	for key, text := range en {
		if strings.TrimSpace(text) == "" {
			continue // empty en_US value is not a usable expectation
		}
		if strings.Contains(text, "{{") {
			continue // placeholders need TemplateData; fallback is not the thing under test
		}
		if _, translated := fa[key]; !translated {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
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
	t.Logf("fa-IR context: %d key(s) fell back to English (%d synthetic + %d real)",
		len(keys), 1, len(keys)-1)
}
