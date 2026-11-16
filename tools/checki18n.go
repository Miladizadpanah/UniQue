//go:build ignore

// Command checki18n verifies that every i18n key used in web/html templates
// exists in the fa_IR and en_US translation files, using the real TOML parser.
//
// Run from the repository root:
//
//	go run tools/checki18n.go
//
// It is a build-ignored one-off rather than a test because it reads the real
// translation files by path; the CI guard for the same property lives in
// web/i18n_toml_test.go, which is embedded and therefore hermetic. This one is
// for a developer who has just edited a template and wants the full key list
// without running the whole suite.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func main() {
	// Collect all i18n keys used in templates.
	htmlDir := "web/html"
	var keys []string
	i18nRe := regexp.MustCompile(`i18n\s+"([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)"`)
	filepath.WalkDir(htmlDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		raw, _ := os.ReadFile(path)
		for _, m := range i18nRe.FindAllStringSubmatch(string(raw), -1) {
			keys = append(keys, m[1])
		}
		return nil
	})
	// Dedupe.
	seen := map[string]bool{}
	var unique []string
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			unique = append(unique, k)
		}
	}
	fmt.Printf("i18n keys used in templates: %d\n", len(unique))

	// Load translation files.
	failed := false
	for _, file := range []string{"web/translation/translate.en_US.toml", "web/translation/translate.fa_IR.toml"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			fmt.Printf("%s: %v\n", file, err)
			failed = true
			continue
		}
		var m map[string]any
		if err := toml.Unmarshal(raw, &m); err != nil {
			fmt.Printf("%s: invalid TOML: %v\n", file, err)
			failed = true
			continue
		}
		// Flatten to dotted keys.
		flat := map[string]bool{}
		flatten("", m, flat)
		// Check which used keys are missing.
		var missing []string
		for _, k := range unique {
			if !flat[k] {
				missing = append(missing, k)
			}
		}
		fmt.Printf("%s: %d keys total, %d missing from templates\n", file, len(flat), len(missing))
		if len(missing) > 0 {
			failed = true
			for _, mk := range missing {
				fmt.Printf("  MISSING: %s\n", mk)
			}
		}
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("every template key is translated in both locales")
}

func flatten(prefix string, m map[string]any, out map[string]bool) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flatten(key, sub, out)
		} else {
			out[key] = true
		}
	}
}
