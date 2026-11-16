package csscheck

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures spell out both failure modes: a class the markup uses but no
// stylesheet defines (the shipped regression), and a class that is only in the
// stylesheet, which must NOT be reported.
func TestMissingBoClasses(t *testing.T) {
	dir := t.TempDir()
	htmlDir := filepath.Join(dir, "html")
	cssDir := filepath.Join(dir, "css")
	for _, d := range []string{filepath.Join(htmlDir, "common"), cssDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	page := `<div class="bo-card bo-card-body"><span class="bo-i-cpu"></span></div>
<a-modal class="bo-client-modal"></a-modal>
<!-- bo-in-a-comment is still a token the walk sees, same as the real test -->
`
	if err := os.WriteFile(filepath.Join(htmlDir, "page.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	partial := `<div class="bo-nested"></div>`
	if err := os.WriteFile(filepath.Join(htmlDir, "common", "part.html"), []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}
	css := `.bo-card { display: block }
.bo-card-body,
.bo-client-modal { padding: 0 }
.bo-unused { color: red }
`
	if err := os.WriteFile(filepath.Join(cssDir, "components.css"), []byte(css), 0o644); err != nil {
		t.Fatal(err)
	}

	used, missing, err := MissingBoClasses(htmlDir, cssDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// bo-card, bo-card-body, bo-client-modal, bo-nested, bo-in-a-comment.
	// bo-i-cpu is an icon id and was skipped.
	if used != 5 {
		t.Fatalf("expected 5 used classes, got %d", used)
	}
	if len(missing) != 2 || missing[0] != "bo-in-a-comment" || missing[1] != "bo-nested" {
		t.Fatalf("expected [bo-in-a-comment bo-nested], got %v", missing)
	}
}

func TestMissingBoClassesCleanTree(t *testing.T) {
	dir := t.TempDir()
	htmlDir := filepath.Join(dir, "html")
	cssDir := filepath.Join(dir, "css")
	if err := os.MkdirAll(htmlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cssDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(htmlDir, "p.html"), []byte(`<div class="bo-row"></div>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cssDir, "tokens.css"), []byte(`:root { --x: 1 } .bo-row { display: flex }`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, missing, err := MissingBoClasses(htmlDir, cssDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected nothing missing, got %v", missing)
	}
}
