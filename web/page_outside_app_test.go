package web

import (
	"bytes"
	"html/template"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/web/htmlscan"
)

// panelPages is every page the panel serves from a controller. settings/* and the
// subscription pages are excluded on purpose: they are rendered with their own
// data by another controller and would report the test data as missing.
//
// Shared by the render guard and the outside-#app guard so a page added to one
// is never silently skipped by the other.
var panelPages = []string{
	"index.html", "inbounds.html", "clients.html", "admins.html",
	"resellers.html", "core.html", "xray.html", "settings.html",
	"nodes.html", "tunnels.html",
}

// TestModalsOutsideAppMountThemselves renders every page and checks the rule
// that makes post-shell_end markup safe: markup after #app is never compiled by
// the page's Vue root, so anything that starts there must mount itself.
//
// Files under web/html/modals obey this by carrying their own
// `new Vue({ el: '#modal-id' })`. An inline <a-modal> dropped after shell_end
// does not, and the failure is silent: the dialog renders as static text with
// literal [[ ]] placeholders and inert v-model, while the API behind it works.
// That is how the clone-to-node dialog shipped. See the htmlscan package.
func TestModalsOutsideAppMountThemselves(t *testing.T) {
	tpl, err := (&Server{}).getHtmlTemplate(template.FuncMap{
		"i18n": func(key string, args ...string) (string, error) { return key, nil },
	})
	if err != nil {
		t.Fatalf("load templates: %v", err)
	}

	for _, page := range panelPages {
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, page, pageRenderData()); err != nil {
			t.Errorf("%s: render failed: %v", page, err)
			continue
		}
		modals, scripts := htmlscan.ModalsOutsideApp(buf.String())
		for _, attrs := range modals {
			id := htmlscan.AttrValue(attrs, "id")
			if id == "" {
				t.Errorf("%s: a top-level <a-modal> sits outside #app and has no id, so Vue never compiles it (move it before page/shell_end): %q",
					page, htmlscan.Condense(attrs))
				continue
			}
			if !htmlscan.MountsID(scripts, id) {
				t.Errorf("%s: <a-modal id=%q> is outside #app and nothing mounts it (no new Vue({el:'#%s'})), so its [[ ]] and v-model never bind",
					page, id, id)
			}
		}
	}
}
