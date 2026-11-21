// Package service: preset blob tests.
//
// The blob shapes are pinned against the panel's own JS model (inbound.js)
// and the sub link generators (genHysteriaLink, genTuicLink, genNaiveLink):
// a key that is spelled differently lands in the stored JSON under a name
// nothing reads and the protocol's Go struct falls back to its zero value —
// an inbound that looks configured and is not.
//
// The tests never touch a database: the preset builders under test are pure
// (x25519 included — it is generated in process now, so no xray binary and no
// database are needed), so a zero-value service is enough.
package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// newPresetService returns a service whose preset builders can run without
// a database connection: the builders in this file touch no table.
func newPresetService() *InboundService {
	return new(InboundService)
}

func TestPresetVlessTlsIsTlsNotReality(t *testing.T) {
	s := newPresetService()
	var in struct {
		Security string `json:"security"`
		TLS      *struct {
			Alpn []string `json:"alpn"`
		} `json:"tlsSettings"`
		Reality *struct{} `json:"realitySettings"`
	}
	blob := s.presetTlsStream()
	if err := json.Unmarshal([]byte(blob), &in); err != nil {
		t.Fatalf("presetTlsStream did not parse: %v", err)
	}
	if in.Security != "tls" {
		t.Fatalf("VLESS+TLS preset stream security = %q, want tls", in.Security)
	}
	if in.Reality != nil {
		t.Errorf("VLESS+TLS preset carries a realitySettings block")
	}
	if in.TLS == nil || len(in.TLS.Alpn) == 0 {
		t.Errorf("VLESS+TLS preset has no tlsSettings.alpn: %s", blob)
	}
}

func TestPresetVlessRealityShape(t *testing.T) {
	s := newPresetService()
	blob := s.presetRealityStream()
	var m map[string]any
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatalf("presetRealityStream did not parse: %v", err)
	}
	if m["security"] != "reality" {
		t.Errorf("reality preset stream security = %v, want reality", m["security"])
	}
	reality, _ := m["realitySettings"].(map[string]any)
	if reality == nil {
		t.Fatal("reality preset has no realitySettings block")
	}
	settings, _ := reality["settings"].(map[string]any)
	if settings == nil {
		t.Errorf("realitySettings lacks the settings sub-object the form's model writes: %s", blob)
	} else if settings["publicKey"] == "" {
		t.Errorf("realitySettings.settings.publicKey is empty")
	}
}

func TestPresetHysteria2Stream(t *testing.T) {
	s := newPresetService()
	blob := s.presetHysteriaStream()
	var m map[string]any
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatalf("presetHysteriaStream did not parse: %v", err)
	}
	if m["network"] != "hysteria" {
		t.Errorf("hysteria2 preset stream network = %v, want hysteria", m["network"])
	}
	if m["security"] != "tls" {
		t.Errorf("hysteria2 preset stream security = %v, want tls", m["security"])
	}
	hysteria, _ := m["hysteriaSettings"].(map[string]any)
	if hysteria == nil {
		t.Errorf("hysteria2 preset has no hysteriaSettings: %s", blob)
	}
	tls, _ := m["tlsSettings"].(map[string]any)
	if tls == nil {
		t.Error("hysteria2 preset has no tlsSettings")
		return
	}
	alpn, _ := tls["alpn"].([]any)
	found := false
	for _, a := range alpn {
		if a == "h3" {
			found = true
		}
	}
	if !found {
		t.Errorf("hysteria2 preset alpn lacks h3: %v", alpn)
	}
}

func TestPresetTuicStream(t *testing.T) {
	s := newPresetService()
	blob := s.presetTuicStream()
	var m map[string]any
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatalf("presetTuicStream did not parse: %v", err)
	}
	if m["network"] != "tuic" {
		t.Errorf("tuic preset stream network = %v, want tuic", m["network"])
	}
	if m["security"] != "tls" {
		t.Errorf("tuic preset stream security = %v, want tls", m["security"])
	}
	if strings.Contains(blob, "realitySettings") {
		t.Errorf("tuic preset must not carry realitySettings: %s", blob)
	}
}

func TestPresetTuicSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetTuicSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetTuicSettings did not parse: %v", err)
	}
	if root["congestionControl"] != "bbr" {
		t.Errorf("tuic preset congestionControl = %v, want bbr", root["congestionControl"])
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("tuic preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["id"] == "" || client["password"] == "" {
		t.Errorf("tuic preset client must carry BOTH a uuid and a password: %v", client)
	}
}

func TestPresetNaiveIsTls(t *testing.T) {
	s := newPresetService()
	stream := s.presetNaiveStream()
	var m map[string]any
	if err := json.Unmarshal([]byte(stream), &m); err != nil {
		t.Fatalf("presetNaiveStream did not parse: %v", err)
	}
	if m["security"] != "tls" {
		t.Errorf("naive preset stream security = %v, want tls", m["security"])
	}

	settings := s.presetNaiveSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(settings), &root); err != nil {
		t.Fatalf("presetNaiveSettings did not parse: %v", err)
	}
	if root["network"] != "tcp" {
		t.Errorf("naive preset settings.network = %v, want tcp", root["network"])
	}
	if _, ok := root["masquerade"]; !ok {
		t.Errorf("naive preset lacks the masquerade block: %s", settings)
	}
}

func TestPresetShadowsocks2022(t *testing.T) {
	s := newPresetService()
	blob := s.presetShadowsocksSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetShadowsocksSettings did not parse: %v", err)
	}
	if root["method"] != "2022-blake3-aes-128-gcm" {
		t.Errorf("shadowsocks preset method = %v, want 2022-blake3-aes-128-gcm", root["method"])
	}
	if root["key"] == "" {
		t.Error("shadowsocks preset has no inbound-level 2022 key")
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("shadowsocks preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["password"] == "" {
		t.Error("shadowsocks preset client has no PSK")
	}
}

func TestPresetHysteria2Settings(t *testing.T) {
	s := newPresetService()
	blob := s.presetHysteriaSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetHysteriaSettings did not parse: %v", err)
	}
	if root["version"] != float64(2) {
		t.Errorf("hysteria2 preset version = %v, want 2", root["version"])
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("hysteria2 preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["auth"] == "" {
		t.Errorf("hysteria2 preset client has no auth: %v", client)
	}
}

// VMess is only usable if the seeded account carries `security`; a client
// entry without it is projected with the empty string, which current clients
// refuse. The panel's own VMessSettings class writes "auto" for exactly that
// reason.
func TestPresetVmessSettings(t *testing.T) {
	s := newPresetService()
	var root map[string]any
	if err := json.Unmarshal([]byte(s.presetVmessSettings()), &root); err != nil {
		t.Fatalf("presetVmessSettings did not parse: %v", err)
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("vmess preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if id, _ := client["id"].(string); id == "" {
		t.Error("vmess preset client has no id")
	}
	if client["security"] != "auto" {
		t.Errorf("vmess preset client security = %v, want auto", client["security"])
	}
}

// The VMess transport is WebSocket over TLS: a plain tcp/none stream would make
// the CDN case (the whole point of a VMess preset) impossible, and a fixed path
// would be one canned string shared by every panel.
func TestPresetVmessStreamIsWsOverTls(t *testing.T) {
	s := newPresetService()
	first := vmessStreamParts(t, s.presetVmessStream())
	second := vmessStreamParts(t, s.presetVmessStream())
	if first.network != "ws" {
		t.Errorf("vmess preset network = %q, want ws", first.network)
	}
	if first.security != "tls" {
		t.Errorf("vmess preset security = %q, want tls", first.security)
	}
	if first.path == "" || !strings.HasPrefix(first.path, "/") {
		t.Errorf("vmess preset ws path = %q, want an absolute path", first.path)
	}
	if first.path == second.path {
		t.Errorf("two VMess presets share the WebSocket path %q", first.path)
	}
	if first.serverName == "" {
		t.Error("vmess preset tlsSettings.serverName is empty")
	}
}

type vmessStreamView struct {
	network, security, path, serverName string
}

func vmessStreamParts(t *testing.T, blob string) vmessStreamView {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatalf("vmess stream did not parse: %v", err)
	}
	view := vmessStreamView{}
	view.network, _ = m["network"].(string)
	view.security, _ = m["security"].(string)
	ws, _ := m["wsSettings"].(map[string]any)
	view.path, _ = ws["path"].(string)
	tls, _ := m["tlsSettings"].(map[string]any)
	view.serverName, _ = tls["serverName"].(string)
	return view
}

// Trojan authenticates the account with its password and nothing else; the
// fallbacks list has to be present (even empty) for the form's own round-trip.
func TestPresetTrojanSettings(t *testing.T) {
	s := newPresetService()
	var root map[string]any
	if err := json.Unmarshal([]byte(s.presetTrojanSettings()), &root); err != nil {
		t.Fatalf("presetTrojanSettings did not parse: %v", err)
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("trojan preset seeds %d clients, want 1", len(clients))
	}
	if pw, _ := clients[0].(map[string]any)["password"].(string); pw == "" {
		t.Error("trojan preset client has no password")
	}
	if _, ok := root["fallbacks"]; !ok {
		t.Error("trojan preset has no fallbacks key; the form's model expects it")
	}
}

// Every catalog entry has to be buildable: a preset whose protocol has no
// settings builder creates a row AddInbound rejects, and the operator gets an
// error from the one button that is supposed to always work.
func TestPresetCatalogEntriesAreSupported(t *testing.T) {
	s := newPresetService()
	seen := map[string]bool{}
	// Protocols that don't use client accounts (port-forwarding, virtual interface)
	noClientAccountProtocols := map[model.Protocol]bool{
		model.Tunnel: true,
		model.Protocol("tun"): true,
	}
	for _, p := range s.Presets() {
		if p.Name == "" || p.Description == "" {
			t.Errorf("preset %+v is missing a name or description", p)
		}
		if seen[p.Name] {
			t.Errorf("duplicate preset name %q", p.Name)
		}
		seen[p.Name] = true
		if !presetBuildable(p.Protocol) {
			t.Errorf("preset %q uses protocol %q, which PresetInbound refuses", p.Name, p.Protocol)
		}
		in := &model.Inbound{Protocol: p.Protocol}
		s.fillPresetDefaults(in, p)
		if strings.TrimSpace(in.Settings) == "" {
			t.Errorf("preset %q built no settings", p.Name)
		}
		if strings.TrimSpace(in.StreamSettings) == "" {
			t.Errorf("preset %q built no stream settings", p.Name)
		}
		var root map[string]any
		if err := json.Unmarshal([]byte(in.Settings), &root); err != nil {
			t.Errorf("preset %q settings are not JSON: %v", p.Name, err)
		}
		// Verify at least one client/account list key exists (except for protocols
		// that don't use client accounts like Tunnel and TUN)
		if !noClientAccountProtocols[p.Protocol] {
			clientKeys := []string{"clients", "wgClients", "accounts"}
			hasClientList := false
			for _, k := range clientKeys {
				if arr, ok := root[k].([]any); ok && len(arr) > 0 {
					hasClientList = true
					break
				}
			}
			if !hasClientList {
				t.Errorf("preset %q has no seeded client/account list (checked: clients, wgClients, accounts)", p.Name)
			}
		}
	}
}

// The two VLESS presets must produce DIFFERENT stream layers; before the
// Security field existed, fillPresetDefaults keyed only on protocol and both
// presets built a reality inbound.
func TestPresetSecurityFieldDrivesTheBlobs(t *testing.T) {
	s := newPresetService()
	presets := s.Presets()
	var vlessTLS, vlessReality *InboundPreset
	for i := range presets {
		if presets[i].Protocol != "vless" {
			continue
		}
		switch presets[i].Security {
		case "tls":
			vlessTLS = &presets[i]
		case "reality":
			vlessReality = &presets[i]
		}
	}
	if vlessTLS == nil || vlessReality == nil {
		t.Fatalf("expected one vless/tls and one vless/reality preset, got: %+v", presets)
	}
	tlsBlob := s.presetTlsStream()
	realityBlob := s.presetRealityStream()
	if strings.Contains(tlsBlob, "realitySettings") {
		t.Errorf("VLESS+TLS stream leaked a realitySettings block: %s", tlsBlob)
	}
	if !strings.Contains(realityBlob, "realitySettings") {
		t.Errorf("VLESS+REALITY stream lacks realitySettings: %s", realityBlob)
	}
}

// ---------------------------------------------------------------- port picker

// The band is the whole contract: outside it a preset can collide with a
// privileged port, the kernel's ephemeral range, or a distro service.
func TestPresetPortCandidatesStayInBand(t *testing.T) {
	candidates := presetPortCandidates()
	if len(candidates) != presetPortHigh-presetPortLow+1 {
		t.Fatalf("candidates = %d, want %d", len(candidates), presetPortHigh-presetPortLow+1)
	}
	seen := map[int]bool{}
	for _, p := range candidates {
		if p < presetPortLow || p > presetPortHigh {
			t.Fatalf("candidate %d outside the %d-%d band", p, presetPortLow, presetPortHigh)
		}
		if seen[p] {
			t.Fatalf("candidate %d appears twice", p)
		}
		seen[p] = true
	}
}

// A port another inbound already binds must never be handed out, even when it
// is the first candidate in the shuffled order.
func TestPickPresetPortSkipsClaimed(t *testing.T) {
	candidates := []int{20001, 20002, 20003, 20004}
	claimed := map[int]bool{20001: true, 20002: true, 20003: true}
	port, err := pickPresetPort(candidates, claimed, nil, 8)
	if err != nil {
		t.Fatalf("pickPresetPort: %v", err)
	}
	if port != 20004 {
		t.Fatalf("port = %d, want the only unclaimed candidate 20004", port)
	}
}

// A socket the host already holds is the same answer as a claimed port: the
// preset must step over it instead of handing AddInbound a port it will refuse.
func TestPickPresetPortSkipsInUse(t *testing.T) {
	candidates := []int{20001, 20002, 20003}
	inUse := func(p int) bool { return p < 20003 }
	port, err := pickPresetPort(candidates, map[int]bool{}, inUse, 8)
	if err != nil {
		t.Fatalf("pickPresetPort: %v", err)
	}
	if port != 20003 {
		t.Fatalf("port = %d, want 20003", port)
	}
}

func TestPickPresetPortExhausted(t *testing.T) {
	candidates := []int{20001, 20002}
	claimed := map[int]bool{20001: true, 20002: true}
	if _, err := pickPresetPort(candidates, claimed, nil, 8); err == nil {
		t.Fatal("pickPresetPort found a port with every candidate claimed")
	}
}

// ------------------------------------------------------------- certificates

// A TLS listener with an empty certificate list starts and then fails every
// handshake. Every TLS-family preset must therefore carry a certificate.
func TestPresetTlsFamilyBindsCertificate(t *testing.T) {
	s := newPresetService()
	cases := map[string]string{
		"VLESS+TLS":    s.presetTlsStream(),
		"Naive":        s.presetNaiveStream(),
		"Hysteria2":    s.presetHysteriaStream(),
		"TUIC":         s.presetTuicStream(),
		"VMess+WS+TLS": s.presetVmessStream(),
		"AnyTLS":       s.presetTlsStream(), // AnyTLS uses presetTlsStream
		"Hysteria v1":  s.presetHysteriaV1Stream(),
	}
	for name, blob := range cases {
		var m map[string]any
		if err := json.Unmarshal([]byte(blob), &m); err != nil {
			t.Fatalf("%s: stream did not parse: %v", name, err)
		}
		tls, _ := m["tlsSettings"].(map[string]any)
		if tls == nil {
			t.Errorf("%s: no tlsSettings", name)
			continue
		}
		certs, _ := tls["certificates"].([]any)
		if len(certs) != 1 {
			t.Errorf("%s: certificates = %v, want exactly one bound block", name, tls["certificates"])
			continue
		}
		block, _ := certs[0].(map[string]any)
		inline, _ := block["certificate"].([]any)
		file, _ := block["certificateFile"].(string)
		if len(inline) == 0 && file == "" {
			t.Errorf("%s: certificate block carries neither inline cert nor file: %v", name, block)
		}
		if len(inline) > 0 {
			pemText, _ := inline[0].(string)
			if !strings.Contains(pemText, "BEGIN CERTIFICATE") {
				t.Errorf("%s: inline certificate is not PEM", name)
			}
		}
		if sni, _ := tls["serverName"].(string); strings.TrimSpace(sni) == "" {
			t.Errorf("%s: tlsSettings.serverName is empty, so links ship no SNI", name)
		}
	}
}

// ------------------------------------------------------------------- reality

func TestPresetRealityServerNamesIsAList(t *testing.T) {
	s := newPresetService()
	var m map[string]any
	if err := json.Unmarshal([]byte(s.presetRealityStream()), &m); err != nil {
		t.Fatalf("presetRealityStream did not parse: %v", err)
	}
	reality, _ := m["realitySettings"].(map[string]any)
	if reality == nil {
		t.Fatal("no realitySettings")
	}
	names, ok := reality["serverNames"].([]any)
	if !ok || len(names) == 0 {
		t.Fatalf("serverNames = %#v, want a non-empty JSON list", reality["serverNames"])
	}
	dest, _ := reality["dest"].(string)
	target, _ := reality["target"].(string)
	sni, _ := names[0].(string)
	if target == "" || target != dest {
		t.Errorf("target %q / dest %q disagree — the form and the core would camouflage to different hosts", target, dest)
	}
	if !strings.HasPrefix(dest, sni+":") {
		t.Errorf("dest %q does not belong to serverName %q — a probe sees forged TLS", dest, sni)
	}
	shortIDs, _ := reality["shortIds"].([]any)
	if len(shortIDs) < 2 {
		t.Fatalf("shortIds = %v, want the empty id plus a random one", reality["shortIds"])
	}
	for _, raw := range shortIDs[1:] {
		id, _ := raw.(string)
		if len(id) != 8 || len(id)%2 != 0 {
			t.Errorf("shortId %q is not an 8-character even-length hex string", id)
		}
		if strings.Trim(id, "0123456789abcdef") != "" {
			t.Errorf("shortId %q contains non-hex characters", id)
		}
	}
	settings, _ := reality["settings"].(map[string]any)
	if spider, _ := settings["spiderX"].(string); spider == "" {
		t.Error("realitySettings.settings.spiderX is empty")
	}
}

// Two presets must not share a key pair or a shortId: the x25519 pair is the
// server's identity, and a duplicated one is a fingerprint shared by every
// panel that installed it.
func TestPresetRealityIsUniquePerCall(t *testing.T) {
	s := newPresetService()
	first := realityParts(t, s.presetRealityStream())
	second := realityParts(t, s.presetRealityStream())
	if first.privateKey == second.privateKey {
		t.Error("two REALITY presets share a privateKey")
	}
	if len(first.shortIds) < 2 || len(second.shortIds) < 2 {
		t.Fatalf("shortIds = %v / %v, want the empty id plus a random one", first.shortIds, second.shortIds)
	}
	if first.shortIds[1] == second.shortIds[1] {
		t.Error("two REALITY presets share a shortId")
	}
}

type realityView struct {
	dest, privateKey, spiderX string
	serverNames, shortIds     []string
}

func realityParts(t *testing.T, blob string) realityView {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatalf("reality stream did not parse: %v", err)
	}
	reality, _ := m["realitySettings"].(map[string]any)
	view := realityView{}
	view.dest, _ = reality["dest"].(string)
	view.privateKey, _ = reality["privateKey"].(string)
	for _, raw := range reality["serverNames"].([]any) {
		view.serverNames = append(view.serverNames, raw.(string))
	}
	for _, raw := range reality["shortIds"].([]any) {
		view.shortIds = append(view.shortIds, raw.(string))
	}
	settings, _ := reality["settings"].(map[string]any)
	view.spiderX, _ = settings["spiderX"].(string)
	return view
}

// An SNI override used to write serverNames as a bare string, which the core
// refuses at config load: the inbound saved and never came up.
func TestApplyPresetSniWritesRealityList(t *testing.T) {
	s := newPresetService()
	inbound := &model.Inbound{StreamSettings: s.presetRealityStream()}
	preset := InboundPreset{Protocol: model.VLESS, Security: "reality"}
	if err := s.applyPresetSni(inbound, preset, "cdn.example.com"); err != nil {
		t.Fatalf("applyPresetSni: %v", err)
	}
	view := realityParts(t, inbound.StreamSettings)
	if len(view.serverNames) != 1 || view.serverNames[0] != "cdn.example.com" {
		t.Fatalf("serverNames = %v, want [cdn.example.com]", view.serverNames)
	}
	if view.dest != "cdn.example.com:443" {
		t.Fatalf("dest = %q, want cdn.example.com:443", view.dest)
	}
}

// -------------------------------------------------------------- the whole row

// The seeded account must be a complete panel client: a bare {email, id} entry
// comes back with no subId, which hides its subscription link and blocks the
// node sync.
//
// Different protocols use different keys for their client/account lists:
// - Most use "clients"
// - WireGuard (Xray) uses "wgClients"
// - Mixed uses "accounts"
// - HTTP uses "accounts"
func TestPresetAccountsCarrySubId(t *testing.T) {
	s := newPresetService()
	cases := map[string]struct {
		blob      string
		clientKey string // key in settings JSON for the client/account list
	}{
		"vless":         {s.presetVlessSettings(true), "clients"},
		"hysteria2":     {s.presetHysteriaSettings(), "clients"},
		"tuic":          {s.presetTuicSettings(), "clients"},
		"naive":         {s.presetNaiveSettings(), "clients"},
		"shadowsocks":   {s.presetShadowsocksSettings(), "clients"},
		"hysteria":      {s.presetHysteriaV1Settings(), "clients"},
		"anytls":        {s.presetAnyTLSSettings(), "clients"},
		"wireguard":     {s.presetWireguardSettings(), "wgClients"},
		"l2tp":          {s.presetL2TPSettings(), "clients"},
		"pptp":          {s.presetPPTPSettings(), "clients"},
		"openvpn":       {s.presetOpenVPNSettings(), "clients"},
		"openconnect":   {s.presetOpenConnectSettings(), "clients"},
		"sstp":          {s.presetSSTPSettings(), "clients"},
		"ikev2":         {s.presetIKEv2Settings(), "clients"},
		"wgc":           {s.presetWGCSettings(), "clients"},
		"awg":           {s.presetAWGSettings(), "clients"},
		"gre":           {s.presetGRESettings(), "clients"},
		"mtproto":       {s.presetMTProtoSettings(), "clients"},
		"ssh":           {s.presetSSHSettings(), "clients"},
		// Mixed and HTTP use "accounts" with user/pass, not subId
		// "mixed":        {s.presetMixedSettings(), "accounts"},
		// "http":         {s.presetHTTPSettings(), "accounts"},
	}
	for name, tc := range cases {
		var root map[string]any
		if err := json.Unmarshal([]byte(tc.blob), &root); err != nil {
			t.Fatalf("%s: settings did not parse: %v", name, err)
		}
		clients, _ := root[tc.clientKey].([]any)
		if len(clients) != 1 {
			t.Fatalf("%s: seeded %d %s, want 1", name, len(clients), tc.clientKey)
		}
		client, _ := clients[0].(map[string]any)
		if subId, _ := client["subId"].(string); len(subId) != 16 {
			t.Errorf("%s: subId = %q, want 16 characters", name, subId)
		}
		if enable, _ := client["enable"].(bool); !enable {
			t.Errorf("%s: seeded client is not enabled", name)
		}
		if _, ok := client["expiryTime"]; !ok {
			t.Errorf("%s: seeded client has no expiryTime field", name)
		}
	}
}

// Every preset must ship a sniffing block: without it domain routing, the
// block lists and the geo rules see nothing and a "ready" inbound is useless.
func TestPresetSniffingEnabled(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(presetSniffing), &m); err != nil {
		t.Fatalf("presetSniffing did not parse: %v", err)
	}
	if on, _ := m["enabled"].(bool); !on {
		t.Error("sniffing is not enabled")
	}
	overrides, _ := m["destOverride"].([]any)
	if len(overrides) == 0 {
		t.Error("sniffing has no destOverride entries")
	}
	for _, raw := range overrides {
		if raw == "fakedns" {
			t.Error("fakedns needs a fakedns DNS server; a preset must not point clients at one that is absent")
		}
	}
}

// --- Tests for new presets (Hysteria v1 .. SSH) ---

func TestPresetHysteriaV1Settings(t *testing.T) {
	s := newPresetService()
	blob := s.presetHysteriaV1Settings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetHysteriaV1Settings did not parse: %v", err)
	}
	if root["version"] != float64(1) {
		t.Errorf("hysteria v1 preset version = %v, want 1", root["version"])
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("hysteria v1 preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["auth"] == "" {
		t.Errorf("hysteria v1 preset client has no auth: %v", client)
	}
}

func TestPresetHysteriaV1Stream(t *testing.T) {
	s := newPresetService()
	blob := s.presetHysteriaV1Stream()
	var m map[string]any
	if err := json.Unmarshal([]byte(blob), &m); err != nil {
		t.Fatalf("presetHysteriaV1Stream did not parse: %v", err)
	}
	if m["network"] != "hysteria" {
		t.Errorf("hysteria v1 preset stream network = %v, want hysteria", m["network"])
	}
	if m["security"] != "tls" {
		t.Errorf("hysteria v1 preset stream security = %v, want tls", m["security"])
	}
	hysteria, _ := m["hysteriaSettings"].(map[string]any)
	if hysteria == nil {
		t.Errorf("hysteria v1 preset has no hysteriaSettings: %s", blob)
	}
	if hysteria["version"] != float64(1) {
		t.Errorf("hysteria v1 preset version = %v, want 1", hysteria["version"])
	}
}

func TestPresetAnyTLSSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetAnyTLSSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetAnyTLSSettings did not parse: %v", err)
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("anytls preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["password"] == "" {
		t.Error("anytls preset client has no password")
	}
	if _, ok := root["paddingScheme"]; !ok {
		t.Error("anytls preset lacks paddingScheme key")
	}
}

func TestPresetWireguardSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetWireguardSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetWireguardSettings did not parse: %v", err)
	}
	// Required keys
	for _, key := range []string{"secretKey", "pubKey", "peers", "noKernelTun", "wgClients", "clientNetwork", "userLimit", "mtu"} {
		if _, ok := root[key]; !ok {
			t.Errorf("wireguard preset missing %q: %s", key, blob)
		}
	}
	if root["secretKey"] == "" || root["pubKey"] == "" {
		t.Error("wireguard preset has empty keypair")
	}
	clients, _ := root["wgClients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("wireguard preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["email"] == "" {
		t.Error("wireguard preset client has no email")
	}
	if client["devices"] == nil {
		t.Error("wireguard preset client has no devices")
	}
}

func TestPresetMixedSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetMixedSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetMixedSettings did not parse: %v", err)
	}
	if root["auth"] != "password" {
		t.Errorf("mixed preset auth = %v, want password", root["auth"])
	}
	accounts, _ := root["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("mixed preset seeds %d accounts, want 1", len(accounts))
	}
	account := accounts[0].(map[string]any)
	if account["user"] == "" || account["pass"] == "" {
		t.Error("mixed preset account missing user/pass")
	}
}

func TestPresetHTTPSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetHTTPSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetHTTPSettings did not parse: %v", err)
	}
	accounts, _ := root["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("http preset seeds %d accounts, want 1", len(accounts))
	}
	account := accounts[0].(map[string]any)
	if account["user"] == "" || account["pass"] == "" {
		t.Error("http preset account missing user/pass")
	}
	if root["allowTransparent"] != false {
		t.Errorf("http preset allowTransparent = %v, want false", root["allowTransparent"])
	}
}

func TestPresetTunnelSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetTunnelSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetTunnelSettings did not parse: %v", err)
	}
	if root["address"] != "127.0.0.1" {
		t.Errorf("tunnel preset address = %v, want 127.0.0.1", root["address"])
	}
	if root["port"] != float64(80) {
		t.Errorf("tunnel preset port = %v, want 80", root["port"])
	}
	if root["network"] != "tcp,udp" {
		t.Errorf("tunnel preset network = %v, want tcp,udp", root["network"])
	}
}

func TestPresetTunSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetTunSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetTunSettings did not parse: %v", err)
	}
	if root["name"] != "xray0" {
		t.Errorf("tun preset name = %v, want xray0", root["name"])
	}
	mtu, _ := root["mtu"].([]any)
	if len(mtu) != 2 || mtu[0] != float64(1500) || mtu[1] != float64(1280) {
		t.Errorf("tun preset mtu = %v, want [1500, 1280]", mtu)
	}
}

func TestPresetL2TPSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetL2TPSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetL2TPSettings did not parse: %v", err)
	}
	// Check VPN defaults present
	for _, key := range []string{"ipsecEnable", "ipsecPsk", "ipRanges", "dns1", "dns2", "mtu", "userLimit"} {
		if _, ok := root[key]; !ok {
			t.Errorf("l2tp preset missing %q", key)
		}
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("l2tp preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["id"] == "" || client["password"] == "" {
		t.Error("l2tp preset client missing id/password")
	}
}

func TestPresetPPTPSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetPPTPSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetPPTPSettings did not parse: %v", err)
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("pptp preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["id"] == "" || client["password"] == "" {
		t.Error("pptp preset client missing id/password")
	}
}

func TestPresetOpenVPNSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetOpenVPNSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetOpenVPNSettings did not parse: %v", err)
	}
	// Check certs embedded
	for _, key := range []string{"caCert", "caKey", "serverCert", "serverKey", "tlsCrypt"} {
		if root[key] == "" {
			t.Errorf("openvpn preset missing inline %q", key)
		}
	}
	if root["tlsUseFile"] != false {
		t.Error("openvpn preset tlsUseFile should be false (inline certs)")
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("openvpn preset seeds %d clients, want 1", len(clients))
	}
}

func TestPresetOpenConnectSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetOpenConnectSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetOpenConnectSettings did not parse: %v", err)
	}
	if root["certificate"] == "" || root["key"] == "" {
		t.Error("openconnect preset missing inline cert/key")
	}
	if root["tlsUseFile"] != false {
		t.Error("openconnect preset tlsUseFile should be false")
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("openconnect preset seeds %d clients, want 1", len(clients))
	}
}

func TestPresetSSTPSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetSSTPSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetSSTPSettings did not parse: %v", err)
	}
	if root["certificate"] == "" || root["key"] == "" {
		t.Error("sstp preset missing inline cert/key")
	}
	if root["tlsUseFile"] != false {
		t.Error("sstp preset tlsUseFile should be false")
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("sstp preset seeds %d clients, want 1", len(clients))
	}
}

func TestPresetIKEv2Settings(t *testing.T) {
	s := newPresetService()
	blob := s.presetIKEv2Settings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetIKEv2Settings did not parse: %v", err)
	}
	if root["caCert"] == "" || root["certificate"] == "" || root["key"] == "" {
		t.Error("ikev2 preset missing inline caCert/certificate/key")
	}
	if root["tlsUseFile"] != false {
		t.Error("ikev2 preset tlsUseFile should be false")
	}
	if root["authMode"] != "eap-mschapv2" {
		t.Errorf("ikev2 preset authMode = %v, want eap-mschapv2", root["authMode"])
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("ikev2 preset seeds %d clients, want 1", len(clients))
	}
}

func TestPresetWGCSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetWGCSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetWGCSettings did not parse: %v", err)
	}
	for _, key := range []string{"dns1", "dns2", "mtu", "serverPrivKey", "serverPubKey", "ipRanges", "userLimit"} {
		if _, ok := root[key]; !ok {
			t.Errorf("wgc preset missing %q", key)
		}
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("wgc preset seeds %d clients, want 1", len(clients))
	}
}

func TestPresetAWGSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetAWGSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetAWGSettings did not parse: %v", err)
	}
	for _, key := range []string{"jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4", "mtu"} {
		if _, ok := root[key]; !ok {
			t.Errorf("awg preset missing %q", key)
		}
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("awg preset seeds %d clients, want 1", len(clients))
	}
}

func TestPresetGRESettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetGRESettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetGRESettings did not parse: %v", err)
	}
	for _, key := range []string{"mtu", "ttl", "ipsecEnable", "ipsecPsk", "allowRaw", "fouEnable", "fouPort", "ipRanges", "userLimit"} {
		if _, ok := root[key]; !ok {
			t.Errorf("gre preset missing %q", key)
		}
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("gre preset seeds %d clients, want 1", len(clients))
	}
}

func TestPresetMTProtoSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetMTProtoSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetMTProtoSettings did not parse: %v", err)
	}
	for _, key := range []string{"modeClassic", "modeSecure", "modeTls", "tlsDomain", "userLimit", "externalProxy"} {
		if _, ok := root[key]; !ok {
			t.Errorf("mtproto preset missing %q", key)
		}
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("mtproto preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["secret"] == "" {
		t.Error("mtproto preset client missing secret")
	}
	if len(client["secret"].(string)) != 32 {
		t.Errorf("mtproto preset secret length = %d, want 32", len(client["secret"].(string)))
	}
}

func TestPresetSSHSettings(t *testing.T) {
	s := newPresetService()
	blob := s.presetSSHSettings()
	var root map[string]any
	if err := json.Unmarshal([]byte(blob), &root); err != nil {
		t.Fatalf("presetSSHSettings did not parse: %v", err)
	}
	for _, key := range []string{"userLimit", "userLimitStrategy", "externalProxy", "hostKey"} {
		if _, ok := root[key]; !ok {
			t.Errorf("ssh preset missing %q", key)
		}
	}
	clients, _ := root["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("ssh preset seeds %d clients, want 1", len(clients))
	}
	client := clients[0].(map[string]any)
	if client["id"] == "" || client["password"] == "" {
		t.Error("ssh preset client missing id/password")
	}
}
