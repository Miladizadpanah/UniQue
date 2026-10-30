package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	xray "github.com/mhsanaei/3x-ui/v2/xray"
)

// The write-path helpers are pure: they decide dial targets and TLS modes for a
// node's remote core API. Locking them down means a config typo fails in the
// save path, not as a silent "node offline".

func TestNormalizeApiTlsMode(t *testing.T) {
	cases := map[string]string{
		"":         xray.ApiTlsInsecure,
		"insecure": xray.ApiTlsInsecure,
		"SKIP":     xray.ApiTlsSkip,
		"verify":   xray.ApiTlsVerify,
		"bogus":    xray.ApiTlsInsecure, // unknown falls back to the LAN-safe mode
	}
	for in, want := range cases {
		if got := normalizeApiTlsMode(in); got != want {
			t.Errorf("normalizeApiTlsMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNodeHasRemoteApi(t *testing.T) {
	if nodeHasRemoteApi(nil) {
		t.Error("nil node must be read-only")
	}
	if nodeHasRemoteApi(&model.Node{}) {
		t.Error("a node without an API port must be read-only")
	}
	if !nodeHasRemoteApi(&model.Node{XrayApiPort: 10085}) {
		t.Error("a node with an API port must be manageable")
	}
}

func TestRemoteApiTarget(t *testing.T) {
	// Explicit host wins over the base url.
	n := &model.Node{BaseUrl: "https://node2.example.com:2096", XrayApiHost: "10.0.0.5", XrayApiPort: 10085}
	if got := remoteApiTarget(n); got != "10.0.0.5:10085" {
		t.Errorf("explicit host: got %q", got)
	}
	// Empty host falls back to the base url's hostname (no port, no scheme).
	n = &model.Node{BaseUrl: "https://node2.example.com:2096", XrayApiPort: 10085}
	if got := remoteApiTarget(n); got != "node2.example.com:10085" {
		t.Errorf("host from base url: got %q", got)
	}
	// IPv4 base url keeps working.
	n = &model.Node{BaseUrl: "http://10.1.2.3:2097", XrayApiPort: 443}
	if got := remoteApiTarget(n); got != "10.1.2.3:443" {
		t.Errorf("ipv4 base url: got %q", got)
	}
}

func TestBaseHostOf(t *testing.T) {
	cases := map[string]string{
		"https://node2.example.com:2096/subs": "node2.example.com",
		"http://10.1.2.3:2097":                "10.1.2.3",
		"not-a-url":                           "",
		"":                                    "",
	}
	for in, want := range cases {
		if got := baseHostOf(in); got != want {
			t.Errorf("baseHostOf(%q) = %q, want %q", in, got, want)
		}
	}
}
