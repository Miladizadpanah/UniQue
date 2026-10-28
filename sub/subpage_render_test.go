package sub

// TestSubpageParsesAndRenders guards the redesigned subscription page: it parses
// the sub server's embedded templates exactly the way setEmbeddedTemplates does
// (same html/template parse of the same three files, with an i18n func) and
// then executes subpage.html with the data map SUBController.subProfile passes
// to c.HTML. This catches template regressions on the public sub page — an
// unbalanced {{ }}, a reference to an undefined template, or a data field the
// controller does not actually provide — without needing a running panel.

import (
	"html/template"
	"strings"
	"testing"

	webpkg "github.com/mhsanaei/3x-ui/v2/web"
)

func TestSubpageParsesAndRenders(t *testing.T) {
	// setEmbeddedTemplates parses with the gin engine's FuncMap, which the sub
	// server registers exactly one custom function onto: i18n. Stub it to
	// return the key so the check is about template structure, not translations.
	funcs := template.FuncMap{"i18n": func(key string, params ...string) string {
		return key
	}}

	tpl, err := template.New("").Funcs(funcs).ParseFS(
		webpkg.EmbeddedHTML(),
		"html/common/page.html",
		"html/component/aThemeSwitch.html",
		"html/settings/panel/subscription/subpage.html",
	)
	if err != nil {
		t.Fatalf("parse sub templates: %v", err)
	}

	page := tpl.Lookup("subpage.html")
	if page == nil {
		t.Fatalf("template subpage.html not registered (Lookup returned nil)")
	}

	// Same map shape as SUBController.subProfile's c.HTML call. .reseller and
	// .perms are read by page/body_start but intentionally omitted here: the
	// real sub controller omits them too, and nil map keys render empty.
	data := map[string]any{
		"title": "subscription.title", "cur_ver": "v2.6.0", "asset_ver": "1",
		"host": "sub.example.com", "base_path": "/subs/", "sId": 42,
		"download": "1.2 GiB", "upload": "3.4 GiB", "total": "10 GiB",
		"used": "4.6 GiB", "remained": "5.4 GiB", "expire": 0,
		"expireDelayDays": 0, "lastOnline": 0, "datepicker": "gregorian",
		"downloadByte": 1288490188, "uploadByte": 3650722255, "totalByte": 10737418240,
		"subUrl": "http://sub.example.com/subs/42", "subJsonUrl": "http://sub.example.com/subs/42/sub/json",
		"subClashUrl": "http://sub.example.com/subs/42/clash",
		"result": []string{"vless://uuid@1.2.3.4:443?encryption=none", "vmess://AAAA"},
		"configs": []SubConfigLink{
			{Label: "OpenVPN", Filename: "client.ovpn", Url: "http://sub.example.com/subs/42/configs/openvpn"},
			{Label: "WireGuard", Filename: "wg.conf", Url: "http://sub.example.com/subs/42/configs/wg"},
		},
	}

	var out strings.Builder
	if err := page.Execute(&out, data); err != nil {
		t.Fatalf("execute subpage.html: %v", err)
	}
	body := out.String()

	// The functional contract subscription.js depends on must survive the
	// redesign: the bootstrap template + links textarea + all three QR canvases.
	for _, want := range []string{
		`id="subscription-data"`, `data-sid="42"`, `id="subscription-links"`,
		`id="qrcode"`, `id="qrcode-subjson"`, `id="qrcode-subclash"`,
		`class="sub-hero"`, `class="sub-ring"`, `class="sub-link-row"`,
		`class="sub-config-btn"`, `vless://uuid@1.2.3.4:443?encryption=none`,
		`download="client.ovpn"`, `subscription.js`, `class="sub-qr-canvas-wrap"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered subpage missing %q", want)
		}
	}
}
