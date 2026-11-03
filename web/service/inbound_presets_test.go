// Package service: preset blob tests.
//
// The blob shapes are pinned against the panel's own JS model (inbound.js)
// and the sub link generators (genHysteriaLink, genTuicLink, genNaiveLink):
// a key that is spelled differently lands in the stored JSON under a name
// nothing reads and the protocol's Go struct falls back to its zero value —
// an inbound that looks configured and is not.
//
// The tests never touch a database: the preset builders under test are pure
// (except x25519, which falls back to a static pair when the xray binary is
// absent, as it is on a CI runner), so a zero-value service is enough.
package service

import (
	"encoding/json"
	"strings"
	"testing"
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
