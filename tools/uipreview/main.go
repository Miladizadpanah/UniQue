// Command uipreview serves the vpn-ui panel's own pages on any machine, so the
// interface can be looked at without a Linux host.
//
// The panel binary is Linux-only: web/service reaches for netlink, unix and
// syscall primitives (RT_TABLE_MAIN, IPPROTO_GRE, syscall.Ucred, ...) that do not
// exist on Windows, so a Windows checkout cannot build it, let alone run it. This
// harness renders the REAL templates from web/html with the REAL front-end assets
// from web/assets and answers the API calls a page makes while it loads from
// fixtures held in memory.
//
// It is a UI preview, not the panel: no Xray, no database, no host services, and
// nothing here is authoritative about server behaviour. What it does exercise
// honestly is the wiring this repository keeps breaking — template data keys,
// runtime helpers, dialog placement inside #app, and the request/response shapes
// the pages themselves send.
//
// Usage, from the repository root:
//
//	go run ./tools/uipreview -addr 127.0.0.1:8899
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	rootDir = flag.String("root", ".", "repository root (holds web/html and web/assets)")
	addr    = flag.String("addr", "127.0.0.1:8899", "listen address")
	lang    = flag.String("lang", "fa_IR", "translation to serve, e.g. fa_IR, en_US")
)

// newI18n is the `i18n` template function. Arguments arrive as "name==value"
// (see locale.createTemplateData), so a translated string containing {{.name}}
// gets its value, and %s is filled in positional order for the older strings.
func newI18n(tr map[string]string) func(string, ...string) string {
	return func(key string, params ...string) string {
		s, found := tr[key]
		if !found {
			s = key
		}
		for _, param := range params {
			parts := strings.SplitN(param, "==", 2)
			if len(parts) != 2 {
				continue
			}
			s = strings.ReplaceAll(s, "{{."+parts[0]+"}}", parts[1])
			s = strings.ReplaceAll(s, "{{ ."+parts[0]+" }}", parts[1])
		}
		for _, param := range params {
			parts := strings.SplitN(param, "==", 2)
			if len(parts) == 2 {
				s = strings.Replace(s, "%s", parts[1], 1)
			}
		}
		return s
	}
}

// ------------------------------------------------------------------ templates

// loadTranslation reads the TOML tables the panel ships and flattens them the way
// go-i18n does: a key inside [pages.inbounds] is addressed as
// "pages.inbounds.<key>". The requested language wins; en_US fills the gaps; a key
// in neither renders as itself, which is what the panel's own localizer falls back
// to as well (see web/locale/locale.go).
func loadTranslation(root, lang string) map[string]string {
	out := map[string]string{}
	for _, name := range []string{lang, "en_US"} {
		path := filepath.Join(root, "web", "translation", "translate."+name+".toml")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		section := ""
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.Trim(line, "[]")
				continue
			}
			eq := strings.Index(line, "=")
			if eq < 0 {
				continue
			}
			key := strings.Trim(strings.TrimSpace(line[:eq]), `"`)
			val := strings.Trim(strings.TrimSpace(line[eq+1:]), `"`)
			if key == "" {
				continue
			}
			if section != "" {
				key = section + "." + key
			}
			if _, seen := out[key]; seen {
				continue
			}
			out[key] = unescape(val)
		}
	}
	return out
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\"`, `"`, `\\`, `\`)
	return r.Replace(s)
}

// loadTemplates parses web/html the way web.getHtmlTemplate does: every file in
// walk order, and a directory whose parse collides with an earlier one is kept as
// it was rather than failing the whole set. The protocol/ and protocol_legacy/
// trees and the nested settings/ trees redeclare file names on purpose — they are
// alternative forms chosen by data at render time — so filtering by file name
// would drop the definitions those alternatives need.
func loadTemplates(root string, i18n func(string, ...string) string) (*template.Template, error) {
	t := template.New("").Funcs(template.FuncMap{"i18n": i18n})
	htmlRoot := filepath.Join(root, "web", "html")
	var files []string
	err := filepath.WalkDir(htmlRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		if _, err := t.ParseFiles(f); err != nil {
			// A name the set already carries, non-empty. The server carries on here
			// too (it ignores a directory's parse error and keeps what it has), and
			// the startup render check below is what catches real damage.
			log.Printf("template %s: %v", f, err)
		}
	}
	return t, nil
}

// ---------------------------------------------------------------------- pages

// pages maps the URLs the panel serves to the template that renders them, from
// the route table in web/controller.
var pages = map[string]string{
	"":          "index.html",
	"index":     "index.html",
	"inbounds":  "inbounds.html",
	"clients":   "clients.html",
	"admins":    "admins.html",
	"resellers": "resellers.html",
	"core":      "core.html",
	"xray":      "xray.html",
	"settings":  "settings.html",
	"tunnel":    "tunnel.html",
	"login":     "login.html",
}

var pageTitles = map[string]string{
	"index.html":     "Overview",
	"inbounds.html":  "Inbounds",
	"clients.html":   "Clients",
	"admins.html":    "Admins",
	"resellers.html": "Resellers",
	"core.html":      "Xray Core",
	"xray.html":      "Xray",
	"settings.html":  "Settings",
	"tunnel.html":    "Tunnel",
	"login.html":     "Login",
}

// permSlugs is model.AllPermissions: the capability map templates read as .perms.
// All of them are granted, so the preview shows the full admin interface.
var permSlugs = []string{
	"accessInbounds", "createInbound", "editInbound", "deleteInbound",
	"createClient", "editClient", "deleteClient", "bulkOperation",
	"accessCoreSettings", "accessXraySettings", "accessPanelSettings",
	"manageResellers", "accessOverview", "manageOverview",
}

// pageData mirrors what web/controller's html() helper puts on every page, plus
// the two keys getContext adds. It is the same set web/page_render_test.go uses,
// so anything a page needs beyond this it fetches at runtime — which is exactly
// what the fixture API below answers.
func pageData(page string) map[string]any {
	perms := map[string]bool{}
	for _, s := range permSlugs {
		perms[s] = true
	}
	return map[string]any{
		"title":       pageTitles[page],
		"host":        *addr,
		"request_uri": "/panel/" + strings.TrimSuffix(page, ".html"),
		// base_path is the prefix the operator configured, and `/panel` is a
		// LITERAL route segment registered by the panel itself (see
		// web/controller/xui.go). The pages put it in front of asset URLs and set
		// it as axios's baseURL, while their API calls carry `/panel/api/...`
		// already — so the default "/" is the only value that resolves both.
		"base_path":  "/",
		"perms":      perms,
		"me":         1,
		"reseller":   map[string]any{"isReseller": false},
		"two_factor": false,
		"cur_ver":    "2.2.6-preview",
		"asset_ver":  "preview",
		"donate":     []any{},
	}
}

// ---------------------------------------------------------------------- store

type inboundGroup struct {
	Id         int    `json:"id"`
	Name       string `json:"name"`
	ParentId   int    `json:"parentId"`
	SortOrder  int    `json:"sortOrder"`
	InboundIds []int  `json:"inboundIds"`
}

type preset struct {
	Id      int    `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// store is the fixture backend. Group writes are implemented for real (in
// memory, with the server's semantics) because the group dialog is one of the
// screens this preview exists to exercise.
type store struct {
	mu      sync.Mutex
	groups  []inboundGroup
	presets []preset
	nextID  int
}

func newStore() *store {
	return &store{
		nextID: 10,
		groups: []inboundGroup{
			{Id: 1, Name: "Germany", SortOrder: 1, InboundIds: []int{1, 2}},
			{Id: 2, Name: "Frankfurt", ParentId: 1, SortOrder: 2, InboundIds: []int{1}},
			{Id: 3, Name: "Netherlands", SortOrder: 3, InboundIds: []int{3}},
		},
		presets: []preset{
			{Id: 1, Name: "Reality 443", Content: `{"protocol":"vless","port":443,"streamSettings":{"network":"tcp","security":"reality"}}`},
			{Id: 2, Name: "VMess WS", Content: `{"protocol":"vmess","port":8080,"streamSettings":{"network":"ws","security":"none"}}`},
		},
	}
}

// inbounds mirrors database/model.Inbound's JSON: the shapes the pages read are
// the model's field names, so these are the same keys the server sends.
func (s *store) inbounds() []map[string]any {
	return []map[string]any{
		{
			"id": 1, "userId": 1, "up": 3 << 30, "down": 21 << 30, "total": 0, "allTime": 24 << 30,
			"remark": "Germany · Reality", "enable": true, "expiryTime": 0, "trafficReset": "never",
			"listen": "", "port": 443, "protocol": "vless", "tag": "inbound-443",
			"trafficMultiplier": 1, "speedLimitDown": 0, "speedLimitUp": 0,
			"ipLimit": 2, "ipLimitStrategy": "reject",
			"settings": `{"clients":[{"id":"7d3e2f1a-0000-4000-8000-000000000001","email":"ali@germany","enable":true,"flow":"xtls-rprx-vision","totalGB":0,"expiryTime":0,"limitIp":2,"subId":"sub-ali","tgId":"","reset":0},` +
				`{"id":"7d3e2f1a-0000-4000-8000-000000000002","email":"sara@germany","enable":true,"flow":"xtls-rprx-vision","totalGB":53687091200,"expiryTime":0,"limitIp":1,"subId":"sub-sara","tgId":"","reset":0}],"decryption":"none","fallbacks":[]}`,
			"streamSettings": `{"network":"tcp","security":"reality","realitySettings":{"dest":"www.cloudflare.com:443","serverNames":["www.cloudflare.com"],"shortIds":["1a2b3c"]}}`,
			"sniffing":       `{"enabled":true,"destOverride":["http","tls"]}`,
			"clientStats": []map[string]any{
				{"id": 1, "inboundId": 1, "enable": true, "email": "ali@germany", "up": 1073741824, "down": 7516192768, "expiryTime": 0, "total": 0, "reset": 0},
				{"id": 2, "inboundId": 1, "enable": true, "email": "sara@germany", "up": 2147483648, "down": 15032385536, "expiryTime": 0, "total": 53687091200, "reset": 0},
			},
		},
		{
			"id": 2, "userId": 1, "up": 1 << 30, "down": 4 << 30, "total": 107374182400, "allTime": 5 << 30,
			"remark": "Frankfurt · VMess WS", "enable": true, "expiryTime": 0, "trafficReset": "monthly",
			"listen": "", "port": 8080, "protocol": "vmess", "tag": "inbound-8080",
			"trafficMultiplier": 1, "ipLimit": 0, "ipLimitStrategy": "reject",
			"settings":       `{"clients":[{"id":"9a1b2c3d-0000-4000-8000-000000000003","email":"reza@frankfurt","enable":true,"alterId":0,"totalGB":107374182400,"expiryTime":0,"limitIp":0,"subId":"sub-reza","tgId":"","reset":0}],"decryption":"none","fallbacks":[]}`,
			"streamSettings": `{"network":"ws","security":"none","wsSettings":{"path":"/ws"}}`,
			"sniffing":       `{"enabled":true,"destOverride":["http","tls"]}`,
			"clientStats": []map[string]any{
				{"id": 3, "inboundId": 2, "enable": true, "email": "reza@frankfurt", "up": 1073741824, "down": 4294967296, "expiryTime": 0, "total": 107374182400, "reset": 0},
			},
		},
		{
			"id": 3, "userId": 1, "up": 512 << 20, "down": 2 << 30, "total": 0, "allTime": 2560 << 20,
			"remark": "Amsterdam · WireGuard", "enable": false, "expiryTime": 0, "trafficReset": "never",
			"listen": "", "port": 51820, "protocol": "wireguard", "tag": "inbound-51820",
			"trafficMultiplier": 1, "ipLimit": 0, "ipLimitStrategy": "reject",
			"settings":       `{"clients":[{"id":"aa11bb22-0000-4000-8000-000000000004","email":"mina@ams","enable":true,"totalGB":0,"expiryTime":0,"limitIp":0,"subId":"sub-mina","tgId":"","reset":0}],"decryption":"none","fallbacks":[]}`,
			"streamSettings": `{"network":"udp","security":"none"}`,
			"sniffing":       `{"enabled":false,"destOverride":[]}`,
			"clientStats": []map[string]any{
				{"id": 4, "inboundId": 3, "enable": true, "email": "mina@ams", "up": 536870912, "down": 2147483648, "expiryTime": 0, "total": 0, "reset": 0},
			},
		},
	}
}

// status is the /panel/api/server/status payload, carrying the keys
// web/service.Status marshals (see server.go), so the overview renders numbers
// rather than empty tiles.
func (s *store) status() map[string]any {
	return map[string]any{
		"cpu": 7.4, "cpuCores": 4, "logicalPro": 8, "cpuSpeedMhz": 2600, "cpuSpeedCurMhz": 3120,
		"cpuModel": "Preview CPU", "osName": "Linux", "osVersion": "6.8.0", "kernel": "6.8.0",
		"hostname": "preview-host",
		"virt":     map[string]any{"vendor": "kvm", "type": "vm"},
		"memHW":    map[string]any{"size": 8589934592, "type": "DDR4", "speed": 3200},
		"mem":      map[string]any{"current": 2147483648, "total": 8589934592},
		"swap":     map[string]any{"current": 0, "total": 2147483648},
		"disk":     map[string]any{"current": 12884901888, "total": 107374182400},
		"xray":     map[string]any{"state": "running", "errorMsg": "", "version": "26.1.1"},
		"uptime":   987654, "loads": []float64{0.12, 0.18, 0.22},
		"netIO":         map[string]any{"up": 1024, "down": 4096},
		"netTraffic":    map[string]any{"up": 12 << 30, "down": 48 << 30},
		"publicIP":      map[string]any{"ipv4": "203.0.113.10", "ipv6": ""},
		"appStats":      map[string]any{"threads": 14, "mem": 73400320, "uptime": 987654},
		"tcpCount":      37,
		"udpCount":      6,
		"cpuCoresShort": 4,
	}
}

func (s *store) defaultSettings() map[string]any {
	return map[string]any{
		"expireDiff": 0, "trafficDiff": 0, "tgBotEnable": false, "ipLimitEnable": true,
		"remarkModel": "-ieo", "datepicker": "gregorian",
		"subEnable": false, "subTitle": "vpn-ui", "subURI": "", "subJsonURI": "", "subJsonEnable": false,
		"xrayAccessLogEnable": false, "geofileAutoUpdate": true,
	}
}

func (s *store) tunnelList() []map[string]any {
	return []map[string]any{
		{
			"id": 1, "name": "tehran-hub", "role": "direct", "protocol": "xhttp",
			"hubHost": "hub.example.com", "hubPort": 443, "enable": true, "remark": "Iran → Germany",
			"certExp": 0,
			"spokes": []map[string]any{
				{"host": "de1.example.com", "port": 443, "lastSeen": 1760000000, "latencyMs": 78},
				{"host": "nl1.example.com", "port": 443, "lastSeen": 0, "latencyMs": 0},
			},
		},
		{
			"id": 2, "name": "backup-spoke", "role": "reverse", "protocol": "tuic",
			"hubHost": "hub2.example.com", "hubPort": 8443, "enable": false, "remark": "",
			"certExp": 0, "spokes": []map[string]any{},
		},
	}
}

func (s *store) tunnelStatus() []map[string]any {
	out := s.tunnelList()
	// Every spoke is probed here; one is deliberately offline so the online/offline
	// styling on the page is visible. lastSeen is read through fmt.Sprint rather
	// than a type assertion: the value crosses encoding/json, so its Go type is
	// whatever JSON chose, and an assertion would panic on a live probe.
	for _, t := range out {
		spokes, ok := t["spokes"].([]map[string]any)
		if !ok {
			continue
		}
		for _, sp := range spokes {
			sp["online"] = atoi(fmt.Sprint(sp["lastSeen"])) != 0
		}
	}
	return out
}

// ------------------------------------------------------------- group handlers

func (s *store) groupsJSON() []inboundGroup {
	return append([]inboundGroup(nil), s.groups...)
}

// saveGroup mirrors InboundGroupService.Create/Update: empty id creates, an id
// renames/moves and replaces the membership.
func (s *store) saveGroup(id int, name string, parentID int, inboundIDs []int) ([]inboundGroup, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("inbound group name is required")
	}
	for _, g := range s.groups {
		if g.Id != id && g.Name == name && g.ParentId == parentID {
			return nil, fmt.Errorf("group name already used")
		}
	}
	if id == 0 {
		s.nextID++
		id = s.nextID
		s.groups = append(s.groups, inboundGroup{
			Id: id, Name: name, ParentId: parentID,
			SortOrder: len(s.groups) + 1, InboundIds: append([]int{}, inboundIDs...),
		})
		return s.groupsJSON(), nil
	}
	for i := range s.groups {
		if s.groups[i].Id == id {
			s.groups[i].Name = name
			s.groups[i].ParentId = parentID
			s.groups[i].InboundIds = append([]int{}, inboundIDs...)
			return s.groupsJSON(), nil
		}
	}
	return nil, fmt.Errorf("group not found")
}

// deleteGroup mirrors InboundGroupService.Delete: subgroups are reparented to the
// deleted group's own parent rather than being carried away with it, and the
// inbounds themselves are never touched.
func (s *store) deleteGroup(id int) ([]inboundGroup, error) {
	parent := -1
	for _, g := range s.groups {
		if g.Id == id {
			parent = g.ParentId
		}
	}
	if parent < 0 {
		return nil, fmt.Errorf("group not found")
	}
	kept := make([]inboundGroup, 0, len(s.groups))
	for _, g := range s.groups {
		if g.Id == id {
			continue
		}
		if g.ParentId == id {
			g.ParentId = parent
		}
		kept = append(kept, g)
	}
	s.groups = kept
	return s.groupsJSON(), nil
}

func (s *store) copyGroup(id int, name string) ([]inboundGroup, error) {
	for _, g := range s.groups {
		if g.Id != id {
			continue
		}
		s.nextID++
		clone := g
		clone.Id = s.nextID
		clone.Name = name
		clone.SortOrder = len(s.groups) + 1
		s.groups = append(s.groups, clone)
		return s.groupsJSON(), nil
	}
	return nil, fmt.Errorf("group not found")
}

func (s *store) reorderGroups(ids []int) []inboundGroup {
	for pos, id := range ids {
		for i := range s.groups {
			if s.groups[i].Id == id {
				s.groups[i].SortOrder = pos + 1
			}
		}
	}
	sort.SliceStable(s.groups, func(a, b int) bool {
		if s.groups[a].SortOrder != s.groups[b].SortOrder {
			return s.groups[a].SortOrder < s.groups[b].SortOrder
		}
		return s.groups[a].Id < s.groups[b].Id
	})
	return s.groupsJSON()
}

// -------------------------------------------------------------------- request

type reqBody struct {
	ID       int             `json:"id"`
	Name     string          `json:"name"`
	ParentID json.RawMessage `json:"parentId"`
	Data     string          `json:"data"`
}

// readBody accepts either the JSON body the pages post or urlencoded fields.
func readBody(r *http.Request) reqBody {
	var b reqBody
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(bytes.TrimSpace(raw)) > 0 {
		if json.Unmarshal(raw, &b) == nil {
			return b
		}
		_ = r.ParseForm()
		if r.PostForm != nil {
			b.Name = r.PostForm.Get("name")
			b.ID = atoi(r.PostForm.Get("id"))
			b.Data = r.PostForm.Get("data")
			return b
		}
	}
	_ = r.ParseForm()
	if r.PostForm != nil {
		b.Name = b.Name + r.PostForm.Get("name")
		if b.ID == 0 {
			b.ID = atoi(r.PostForm.Get("id"))
		}
		if b.Data == "" {
			b.Data = r.PostForm.Get("data")
		}
	}
	return b
}

func (b reqBody) parent() int {
	if len(b.ParentID) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(b.ParentID, &n) != nil {
		var s string
		if json.Unmarshal(b.ParentID, &s) == nil {
			return atoi(s)
		}
	}
	return n
}

func (b reqBody) inboundIDs() []int {
	if strings.TrimSpace(b.Data) == "" {
		return nil
	}
	var ids []int
	if json.Unmarshal([]byte(b.Data), &ids) != nil {
		return nil
	}
	return ids
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// --------------------------------------------------------------------- server

type preview struct {
	tpl   *template.Template
	store *store
	root  string
}

func writeJSON(w http.ResponseWriter, obj any, ok bool, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	out := map[string]any{"success": ok, "msg": msg, "obj": obj}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
}

// once swallows a second body write. The switch below answers the calls the
// pages make; anything it recognises must not also fall through to the stubbed
// reply at the end, and "the first answer wins" is cheaper to guarantee here than
// a return after every case.
type once struct {
	http.ResponseWriter
	wrote bool
}

func (o *once) Write(b []byte) (int, error) {
	if o.wrote {
		return len(b), nil
	}
	o.wrote = true
	return o.ResponseWriter.Write(b)
}

func (p *preview) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	s := p.store
	s.mu.Lock()
	defer s.mu.Unlock()
	first := &once{ResponseWriter: w}
	w = first

	switch {
	case path == "/panel/setting/defaultSettings":
		writeJSON(w, s.defaultSettings(), true, "")

	// ---- inbounds -----------------------------------------------------------
	case path == "/panel/api/inbounds/list":
		writeJSON(w, s.inbounds(), true, "")

	case path == "/panel/api/inbounds/groups":
		if r.Method == http.MethodPost {
			b := readBody(r)
			groups, err := s.saveGroup(b.ID, b.Name, b.parent(), b.inboundIDs())
			if err != nil {
				writeJSON(w, nil, false, err.Error())
				return
			}
			writeJSON(w, groups, true, "")
			return
		}
		writeJSON(w, s.groupsJSON(), true, "")

	case strings.HasPrefix(path, "/panel/api/inbounds/groups/del/"):
		groups, err := s.deleteGroup(atoi(strings.TrimPrefix(path, "/panel/api/inbounds/groups/del/")))
		if err != nil {
			writeJSON(w, nil, false, err.Error())
			return
		}
		writeJSON(w, groups, true, "Group deleted")

	case strings.HasPrefix(path, "/panel/api/inbounds/groups/copy/"):
		b := readBody(r)
		groups, err := s.copyGroup(atoi(strings.TrimPrefix(path, "/panel/api/inbounds/groups/copy/")), b.Name)
		if err != nil {
			writeJSON(w, nil, false, err.Error())
			return
		}
		writeJSON(w, groups, true, "")

	case path == "/panel/api/inbounds/groups/reorder":
		var ids []int
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(raw, &ids)
		writeJSON(w, s.reorderGroups(ids), true, "")

	case path == "/panel/api/inbounds/presets":
		if r.Method == http.MethodPost {
			b := readBody(r)
			s.nextID++
			s.presets = append(s.presets, preset{Id: s.nextID, Name: b.Name, Content: b.Data})
			writeJSON(w, nil, true, "")
			return
		}
		writeJSON(w, s.presets, true, "")

	case strings.HasPrefix(path, "/panel/api/inbounds/presets/del/"):
		id := atoi(strings.TrimPrefix(path, "/panel/api/inbounds/presets/del/"))
		kept := make([]preset, 0, len(s.presets))
		for _, pr := range s.presets {
			if pr.Id != id {
				kept = append(kept, pr)
			}
		}
		s.presets = kept
		writeJSON(w, nil, true, "")

	case strings.HasPrefix(path, "/panel/api/inbounds/del/"):
		id := atoi(strings.TrimPrefix(path, "/panel/api/inbounds/del/"))
		rows := s.inbounds()
		kept := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if row["id"] != id {
				kept = append(kept, row)
			}
		}
		writeJSON(w, kept, true, "")

	case path == "/panel/api/inbounds/reorder":
		writeJSON(w, nil, true, "")

	// ---- clients ------------------------------------------------------------
	case path == "/panel/api/inbounds/onlines":
		writeJSON(w, []string{"ali@germany", "reza@frankfurt"}, true, "")
	case path == "/panel/api/inbounds/onlineMemberships":
		writeJSON(w, []any{}, true, "")
	case path == "/panel/api/inbounds/lastOnline":
		writeJSON(w, map[string]any{"ali@germany": 1760000000, "reza@frankfurt": 1759999000}, true, "")
	case path == "/panel/api/inbounds/resellerBalance":
		writeJSON(w, map[string]any{"isReseller": false}, true, "")
	case path == "/panel/api/clients/list":
		writeJSON(w, map[string]any{"rows": p.clientRows(), "total": 4, "sort": "newest"}, true, "")
	case path == "/panel/api/clients/assignable":
		writeJSON(w, []any{}, true, "")

	// ---- overview / core ----------------------------------------------------
	case path == "/panel/api/server/status":
		writeJSON(w, s.status(), true, "")
	case path == "/panel/xray/getXrayResult":
		// A string, not an array: the page calls .length on it to decide whether the
		// last restart reported anything.
		writeJSON(w, "", true, "")

	case path == "/panel/setting/getDefaultJsonConfig":
		writeJSON(w, `{"inbounds":[],"outbounds":[],"log":{"loglevel":"warning"},"inbounds":[]}`, true, "")

	case path == "/panel/core/status":
		writeJSON(w, []map[string]any{{"name": "xray", "state": "running", "version": "26.1.1", "errorMsg": ""}}, true, "")
	case path == "/panel/api/server/userStats":
		writeJSON(w, map[string]any{"totalUsers": 4, "activeUsers": 2, "expiredUsers": 0, "depletedUsers": 0}, true, "")
	case path == "/panel/api/server/serverName":
		writeJSON(w, map[string]any{"name": "vpn-ui preview"}, true, "")
	case path == "/panel/api/server/panelLocation":
		writeJSON(w, map[string]any{"ip": "203.0.113.10", "country": "DE", "city": "Frankfurt"}, true, "")
	case path == "/panel/api/server/geofileStatus":
		// An ARRAY of per-file states: index.html keys it by state.name and
		// throws a render loop if it is anything else.
		writeJSON(w, []map[string]any{
			{"name": "geoip.dat", "installed": true, "size": 78433210, "updatedAt": 1760000000},
			{"name": "geosite.dat", "installed": true, "size": 12893440, "updatedAt": 1760000000},
		}, true, "")

	case path == "/panel/api/server/geofileProgress",
		path == "/panel/api/server/distroStatus",
		path == "/panel/api/server/updateProgress",
		path == "/panel/api/server/cpuHistory/2":
		writeJSON(w, map[string]any{"running": false}, true, "")

	// ---- tunnel -------------------------------------------------------------
	case path == "/panel/tunnel/list":
		writeJSON(w, s.tunnelList(), true, "")
	case path == "/panel/tunnel/status":
		writeJSON(w, s.tunnelStatus(), true, "")
	}

	// Anything the switch did not recognise: answer the envelope, empty. A page
	// that only needed a successful reply keeps working, and the request is logged
	// so it is visible which endpoint a screen asked for that the harness does not
	// model.
	if first.wrote {
		return
	}
	log.Printf("api (unmodelled): %s %s", r.Method, path)
	writeJSON(w, nil, true, "")
}

// clientRows builds the /panel/api/clients/list rows from the fixture inbounds,
// so the Clients table has something real to render.
func (p *preview) clientRows() []map[string]any {
	var rows []map[string]any
	for _, inb := range p.store.inbounds() {
		var settings struct {
			Clients []map[string]any `json:"clients"`
		}
		if json.Unmarshal([]byte(inb["settings"].(string)), &settings) != nil {
			continue
		}
		for _, c := range settings.Clients {
			rows = append(rows, map[string]any{
				"email":       c["email"],
				"enable":      c["enable"],
				"up":          inb["up"],
				"down":        inb["down"],
				"total":       c["totalGB"],
				"expiryTime":  c["expiryTime"],
				"memberships": []map[string]any{{"inboundId": inb["id"], "remark": inb["remark"], "protocol": inb["protocol"], "enable": true}},
			})
		}
	}
	return rows
}

func (p *preview) handlePage(w http.ResponseWriter, r *http.Request, page string) {
	data := pageData(page)
	var buf bytes.Buffer
	if err := p.tpl.ExecuteTemplate(&buf, page, data); err != nil {
		http.Error(w, fmt.Sprintf("render %s: %v", page, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (p *preview) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Real front-end assets, straight off disk: the same files the panel embeds.
	assets := http.FileServer(http.Dir(filepath.Join(p.root, "web", "assets")))
	mux.Handle("/assets/", http.StripPrefix("/assets/", assets))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/" || path == "/panel" || path == "/panel/":
			p.handlePage(w, r, "index.html")
			return
		case path == "/panel/ws":
			http.Error(w, "no websocket in the preview harness", http.StatusNotFound)
			return
		case strings.HasPrefix(path, "/panel/"):
			rest := strings.TrimPrefix(path, "/panel/")
			if name, ok := pages[strings.TrimSuffix(rest, "/")]; ok {
				p.handlePage(w, r, name)
				return
			}
			// API-ish paths are anything the page fetches, which the panel serves
			// from several controllers rather than one prefix.
			if strings.Contains(rest, "api/") || strings.HasPrefix(rest, "tunnel/") ||
				strings.HasPrefix(rest, "core/") || strings.HasPrefix(rest, "setting/") ||
				strings.HasPrefix(rest, "xray/") || strings.HasPrefix(rest, "server/") ||
				strings.HasPrefix(rest, "resellers/") {
				p.handleAPI(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// selfCheck renders every page once and reports what came out, because a
// template hole is invisible until the page is executed (that is exactly how the
// Tunnel page shipped dead — see web/page_render_test.go).
func (p *preview) selfCheck() bool {
	ok := true
	names := make([]string, 0, len(pageTitles))
	for name := range pageTitles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var buf bytes.Buffer
		if err := p.tpl.ExecuteTemplate(&buf, name, pageData(name)); err != nil {
			log.Printf("SELF-CHECK FAIL %s: %v", name, err)
			ok = false
			continue
		}
		out := buf.String()
		for _, bad := range []string{"<no value>", "i18n("} {
			if i := strings.Index(out, bad); i >= 0 {
				start := i - 40
				if start < 0 {
					start = 0
				}
				end := i + 60
				if end > len(out) {
					end = len(out)
				}
				log.Printf("SELF-CHECK FAIL %s: %q in %q", name, bad, strings.ReplaceAll(out[start:end], "\n", " "))
				ok = false
			}
		}
	}
	return ok
}

func main() {
	flag.Parse()
	log.SetFlags(0)

	tr := loadTranslation(*rootDir, *lang)
	log.Printf("translations: %d keys (%s)", len(tr), *lang)

	tpl, err := loadTemplates(*rootDir, newI18n(tr))
	if err != nil {
		log.Fatalf("load templates: %v", err)
	}

	p := &preview{tpl: tpl, store: newStore(), root: *rootDir}
	if !p.selfCheck() {
		log.Printf("WARNING: a page did not render cleanly; see SELF-CHECK FAIL above")
	} else {
		log.Printf("self-check: every page renders without unresolved markers")
	}

	log.Printf("preview panel on http://%s/panel/  (UI preview only: no Xray, no database)", *addr)
	if err := http.ListenAndServe(*addr, p.routes()); err != nil {
		log.Fatal(err)
	}
}
