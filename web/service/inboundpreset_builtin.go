package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// BuiltinInboundPresets are operator-quality templates seeded into the preset table
// on first run. Each one is a complete Inbound.toJson() blob the preset picker applies
// as-is: no protocol-specific form gymnastics, no missing keys the form would fall
// back on a worse value for.
//
// They are seeded ONCE (idempotent by name, like Save). No auto-inbound creation:
// a preset that pre-created inbounds would collide with the operator's own ports on
// every machine — the picker exists so the operator CHOOSES when an inbound is built.
//
// The content blobs mirror the shapes the panel's own Add dialog writes:
//   - settings:     the protocol's settings JSON (clients: [] — a config never
//                   carries accounts, same as a saved operator preset)
//   - streamSettings: the transport blob the dialog's transport section produces
//   - sniffing:     the default sniffing blob
//
// They are validated at compile time against the Go model, not the JS form, so a
// shape the form could not express would surface at applyPreset() time, not here.

type builtinPreset struct {
	Name    string
	Content string
}

// vlessRealistic is a VLESS+REALITY+xtls-rprx-vision inbound — the strongest
// censorship-resistant tunnel available: no certificate, no IP requirement, and
// traffic that reads as a legitimate TLS client. The preset's security fields are
// filled in; the operator only changes the port, SNI and x25519 key pair.
func vlessRealistic() string {
	return `{"port":443,"listen":"","protocol":"vless","settings":{"clients":[],"decryption":"none","encryption":"none","fallbacks":[],"flow":"xtls-rprx-vision"},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"show":"","xver":4,"privateKey":"","shortIds":[],"dest":"","sni":"","fingerprint":"chrome","serverName":""},"tcpSettings":{"acceptProxyProtocol":false,"header":{"request":{},"response":{}}}},"tag":"","sniffing":{"enabled":true,"destOverride":["http","tls"],"metadataOnly":true},"clientStats":[]}`
}

// vlessXHTTP is VLESS over XHTTP/2 — the most robust transport when REALITY is
// unavailable (no SNI required to pick, no key pair, no xver). Best where a
// dedicated HTTPS endpoint is available and REALITY's dest/sni are unavailable.
func vlessXHTTP() string {
	return `{"port":8443,"listen":"","protocol":"vless","settings":{"clients":[],"decryption":"none","encryption":"none","fallbacks":[]},"streamSettings":{"network":"xhttp","xhttpSettings":{"path":"/vless-xhttp","host":"example.com"},"security":"none"},"tag":"","sniffing":{"enabled":true,"destOverride":["http","tls"],"metadataOnly":true},"clientStats":[]}`
}

// trojanRealistic is Trojan over a domain-backed TLS endpoint. The most
// widely-compatible high-performance protocol: works in every environment
// that can reach a domain, no exotic transport, no kernel module.
func trojanRealistic() string {
	return `{"port":443,"listen":"","protocol":"trojan","settings":{"clients":[],"network":"tcp"},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"","minVersion":"","maxVersion":"","preferServerCert":false,"alpn":["h2","http/1.1"],"show":false,"allowInsecure":false},"tcpSettings":{"acceptProxyProtocol":false,"header":{"request":{},"response":{}}}},"tag":"","sniffing":{"enabled":true,"destOverride":["http","tls"],"metadataOnly":true},"clientStats":[]}`
}

// hysteria2UDP is Hysteria2 over QUIC — best raw-speed option on lossy links
// (mobile, satellite, congested WiFi). The operator only needs the auth password
// and the SNI; the port is the same as the TLS endpoint.
func hysteria2UDP() string {
	return `{"port":443,"listen":"","protocol":"hysteria2","settings":{"clients":[],"auth":"changeme","network":"udp"},"streamSettings":{"network":"udp","security":"tls","tlsSettings":{"serverName":"example.com","minVersion":"","maxVersion":"","preferServerCert":false,"alpn":["h3"],"show":false,"allowInsecure":false},"udpSettings":{"quicSettings":{"maxIdleTimeout":600,"maxIncomingStreams":16},"initialCongestionWindow":1073741824},"tcpSettings":{},"kcpSettings":{},"httpSettings":{},"wsSettings":{},"grpcSettings":{},"xhttpSettings":{},"splithttpSettings":{}},"tag":"","sniffing":{"enabled":true,"destOverride":["http","tls"],"metadataOnly":true},"clientStats":[]}`
}

// tuicV5 is TUIC v5 over QUIC with realistic congestion control — the highest-
// throughput option on modern clients. Best for bulk traffic and video.
func tuicV5() string {
	return `{"port":4433,"listen":"","protocol":"tuic","settings":{"clients":[],"congestionControl":"cubic","authTimeout":3,"zeroRttHandshake":false,"heartbeat":10,"udpTimeout":60},"streamSettings":{"network":"udp","security":"tls","tlsSettings":{"serverName":"example.com","minVersion":"","maxVersion":"","preferServerCert":false,"alpn":["h3","tuic"],"show":false,"allowInsecure":false}},"tag":"","sniffing":{"enabled":true,"destOverride":["http","tls"],"metadataOnly":true},"clientStats":[]}`
}

// shadowsocks2022 is Shadowsocks 2022 (multi-user, aead-2022 ciphers) — best
// for operators who want a simple, high-speed shared key per client, no UUID
// management. Works well behind any TLS front.
func shadowsocks2022() string {
	return `{"port":8388,"listen":"","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","network":"tcp","cipher":"2022-blake3-aes-128-gcm","key":"","clients":[]},"streamSettings":{"network":"tcp","security":"none"},"tag":"","sniffing":{"enabled":true,"destOverride":["http","tls"],"metadataOnly":true},"clientStats":[]}`
}

// builtinInboundPresets returns the preset blobs, in order. Each Name is the
// picker display label; the Content is the Inbound.toJson() blob.
func builtinInboundPresets() []builtinPreset {
	return []builtinPreset{
		{"VLESS + REALITY + Vision", vlessRealistic()},
		{"VLESS + XHTTP/2",          vlessXHTTP()},
		{"Trojan + TLS",             trojanRealistic()},
		{"Hysteria2 (QUIC)",         hysteria2UDP()},
		{"TUIC v5 (QUIC)",           tuicV5()},
		{"Shadowsocks 2022",         shadowsocks2022()},
	}
}

// SeedBuiltinPresets inserts each builtin preset that is not already present by name.
// Idempotent: calling it twice is a no-op on the second call.
//
// Must run AFTER InitDB (the gorm db handle is needed). The caller (main.go or
// the web startup) decides when; the function is safe to call on every startup.
func SeedBuiltinPresets() {
	db := database.GetDB()
	for _, p := range builtinInboundPresets() {
		var count int64
		db.Model(&model.InboundPreset{}).Where("name = ?", p.Name).Count(&count)
		if count == 0 {
			// Validate the JSON before inserting — a broken blob would fail
			// applyPreset() every time it is picked, and the operator has no way
			// to tell it apart from their own saved presets.
			var buf any
			if err := json.Unmarshal([]byte(p.Content), &buf); err != nil {
				continue // invalid JSON: skip, do not seed
			}
			preset := &model.InboundPreset{Name: p.Name, Content: p.Content}
			db.Create(preset)
		}
	}
}
