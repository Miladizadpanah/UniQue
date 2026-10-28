//go:build ignore

// renderpages is a LOCAL development check, not part of the build: it renders the
// panel's pages exactly the way the server does (same embedded templates, same
// i18n func, same page data) and writes them to the directory given as argv[1].
//
// Why it exists: web/service uses unix.*/netlink, so `go test ./web/...` cannot
// compile on a Windows host at all — this is the only way to see what the server
// actually sends before pushing to CI, and it is what turned the tunnel page's
// dead {{ .i18n_fn }} placeholder into a reproducible 40-line finding. The
// committed guard for that is web/page_render_test.go, which the Linux CI runs.
//
// Usage: go run web/renderpages.go /tmp/pages
package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:html
var htmlFS embed.FS

func main() {
	out := "."
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	funcMap := template.FuncMap{
		"i18n": func(key string, args ...string) (string, error) { return key, nil },
	}
	tpl := template.New("").Funcs(funcMap)
	err := fs.WalkDir(htmlFS, "html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			newT, perr := tpl.ParseFS(htmlFS, path+"/*.html")
			if perr != nil {
				return nil // same silent tolerance as production
			}
			tpl = newT
		}
		return nil
	})
	if err != nil {
		panic(err)
	}

	data := map[string]any{
		"title": "Page", "host": "panel.local", "request_uri": "/panel/",
		"base_path": "/", "perms": map[string]bool{}, "me": 1,
		"reseller": map[string]any{}, "two_factor": false,
		"cur_ver": "0.0.0", "asset_ver": "0.0.0-local", "donate": []any{},
	}

	pages := []string{"inbounds.html", "clients.html", "index.html", "admins.html", "resellers.html", "core.html", "xray.html", "settings.html", "nodes.html"}
	for _, page := range pages {
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, page, data); err != nil {
			fmt.Printf("FAIL %-16s %v\n", page, err)
			continue
		}
		s := buf.String()
		flags := []string{}
		if i := strings.Index(s, "{{"); i >= 0 {
			flags = append(flags, fmt.Sprintf("UNRENDERED@%d:%q", i, window(s, i)))
		}
		if i := strings.Index(s, "<no value>"); i >= 0 {
			flags = append(flags, fmt.Sprintf("NOVALUE@%d:%q", i, window(s, i)))
		}
		if i := strings.Index(s, "i18n("); i >= 0 {
			flags = append(flags, fmt.Sprintf("RUNTIME_I18N@%d:%q", i, window(s, i)))
		}
		status := "ok"
		if len(flags) > 0 {
			status = "PROBLEM " + strings.Join(flags, " ")
		}
		dst := filepath.Join(out, page)
		if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("%-16s %7d bytes  %s\n", page, len(s), status)
	}
}

func window(s string, i int) string {
	start := i - 45
	if start < 0 {
		start = 0
	}
	end := i + 65
	if end > len(s) {
		end = len(s)
	}
	return strings.ReplaceAll(s[start:end], "\n", " ")
}
