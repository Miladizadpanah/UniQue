package web

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v2/web/csscheck"
)

// TestBOMarkupClassesAreStyled fails when a template carries a bo-* class that no
// stylesheet defines.
//
// components.css v2 replaced the previous file wholesale and silently dropped
// ~260 classes the templates still used. Nothing failed: the markup rendered, just
// without any of its layout, so cards stacked as plain divs and the quota meters
// vanished. A build cannot see that, and neither can a unit test that only parses
// templates, so the classes themselves are checked against the stylesheets. The
// scan lives in web/csscheck so it can also be run on its own.
func TestBOMarkupClassesAreStyled(t *testing.T) {
	used, missing, err := csscheck.MissingBoClasses("html", "assets/css")
	if err != nil {
		t.Fatalf("scan bo-* classes: %v", err)
	}
	// A walk that found almost nothing is a broken walk, not a clean tree.
	if used < 100 {
		t.Fatalf("only %d bo-* classes found in markup; the walk is not seeing the templates", used)
	}
	if len(missing) > 0 {
		t.Errorf("%d bo-* class(es) used by templates but defined in no stylesheet: %v",
			len(missing), missing)
	}
}
