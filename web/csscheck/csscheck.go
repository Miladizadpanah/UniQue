// Package csscheck cross-checks the bo-* class names the templates use against
// the class names the stylesheets define.
//
// components.css v2 replaced the previous file wholesale and silently dropped
// ~260 classes the templates still used. Nothing failed: the markup rendered,
// just without any of its layout, so cards stacked as plain divs and the quota
// meters vanished. A build cannot see that, and neither can a test that only
// parses templates, so the classes themselves are compared.
//
// The package is pure file reading, so the same check runs in CI (via the web
// test) and on any developer machine.
package csscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// bo- class tokens in markup. Trailing dashes are not included so a prefix
	// built in JS ("bo-if-" + name) is not read as a class called "bo-if-".
	boClassUseRe = regexp.MustCompile(`bo-[a-z0-9]+(?:-[a-z0-9]+)*`)
	cssClassDefRe = regexp.MustCompile(`\.([-_a-zA-Z][-_a-zA-Z0-9]*)`)
)

// UsedBoClasses returns every bo-* class the templates under htmlDir mention.
// SVG icon ids (#bo-i-cpu, and the '#bo-i-' + name prefix) are skipped: they are
// ids, not classes.
func UsedBoClasses(htmlDir string) (map[string]bool, error) {
	used := map[string]bool{}
	err := fs.WalkDir(os.DirFS(htmlDir), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		raw, err := os.ReadFile(filepath.Join(htmlDir, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		for _, c := range boClassUseRe.FindAllString(string(raw), -1) {
			if c == "bo-i" || strings.HasPrefix(c, "bo-i-") {
				continue
			}
			used[c] = true
		}
		return nil
	})
	return used, err
}

// DefinedClasses returns every class name any stylesheet in cssDir defines.
func DefinedClasses(cssDir string) (map[string]bool, error) {
	defined := map[string]bool{}
	entries, err := os.ReadDir(cssDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".css") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(cssDir, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, m := range cssClassDefRe.FindAllStringSubmatch(string(raw), -1) {
			defined[m[1]] = true
		}
	}
	return defined, nil
}

// MissingBoClasses returns the used bo-* classes that no stylesheet defines,
// sorted. htmlDir and cssDir are absolute or relative to the caller's cwd.
func MissingBoClasses(htmlDir, cssDir string) (used int, missing []string, err error) {
	usedSet, err := UsedBoClasses(htmlDir)
	if err != nil {
		return 0, nil, err
	}
	defined, err := DefinedClasses(cssDir)
	if err != nil {
		return 0, nil, err
	}
	for c := range usedSet {
		if !defined[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	return len(usedSet), missing, nil
}
