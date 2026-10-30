package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	xray "github.com/mhsanaei/3x-ui/v2/xray"
)

// InboundPreset is a ready-to-use inbound template with optimized defaults.
// PresetInbound goes through the panel's own AddInbound, so a preset gets the
// same treatment a typed one would — validation, tag normalization, core
// availability, accounts-layer projection and the per-protocol daemons a raw
// db.Create would skip and leave the inbound dark until someone touched it.
type InboundPreset struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Protocol    model.Protocol `json:"protocol"`
	Recommended bool           `json:"recommended"`
	// Security names the stream layer the preset builds: "reality", "tls" or
	// "" (none). One protocol (VLESS) ships two presets differing only here,
	// so the field disambiguates the blobs the preset actually writes.
	Security string `json:"security"`
}

// Presets lists all available inbound presets, best connection first.
func (s *InboundService) Presets() []InboundPreset {
	return []InboundPreset{
		{
			Name:        "VLESS + REALITY",
			Description: "Best censorship resistance. No domain needed. XTLS Vision + multi-SNI camouflage over Cloudflare.",
			Protocol:    model.VLESS,
			Recommended: true,
			Security:    "reality",
		},
		{
			Name:        "Hysteria2",
			Description: "High-speed UDP protocol with QUIC. Best for mobile and lossy networks.",
			Protocol:    model.Hysteria2,
			Recommended: true,
		},
		{
			Name:        "VLESS + TLS",
			Description: "Standard TLS. Needs a certificate; pairs well with a CDN front.",
			Protocol:    model.VLESS,
			Recommended: false,
			Security:    "tls",
		},
		{
			Name:        "TUIC v5",
			Description: "Next-gen UDP protocol with BBR congestion control. Excellent throughput.",
			Protocol:    model.TUIC,
			Recommended: false,
		},
		{
			Name:        "NaiveProxy",
			Description: "Plain-HTTPS obfuscation. Good where TLS fingerprints are inspected.",
			Protocol:    model.NAIVE,
			Recommended: false,
			Security:    "tls",
		},
		{
			Name:        "Shadowsocks 2022",
			Description: "Shadowsocks with the 2022-blake3 ciphers, no legacy guesswork.",
			Protocol:    model.Shadowsocks,
			Recommended: false,
		},
	}
}

// PresetInbound creates one inbound from a preset through the panel's own
// create path (AddInbound), so the preset row is indistinguishable from a
// typed one. It returns the persisted row, whether the running core needs a
// restart, and the error (if any).
func (s *InboundService) PresetInbound(preset InboundPreset, remark string) (*model.Inbound, bool, error) {
	if !nodeMintable(preset.Protocol) {
		return nil, false, fmt.Errorf("protocol %s has no preset", preset.Protocol)
	}

	port, err := s.presetInboundPort()
	if err != nil {
		return nil, false, err
	}

	inbound := &model.Inbound{
		Protocol: preset.Protocol,
		Listen:   "0.0.0.0",
		Port:     port,
		Enable:   true,
		Remark:   strings.TrimSpace(remark),
	}
	if inbound.Remark == "" {
		inbound.Remark = preset.Name
	}
	// The preset's optimized settings + streamSettings + one seeded account,
	// minted the way the operator would have typed them but with values that
	// measurably connect best.
	s.fillPresetDefaults(inbound, preset)

	saved, needRestart, err := s.AddInbound(inbound)
	if err != nil {
		return nil, false, err
	}
	logger.Info("preset inbound created:", saved.Tag, saved.Protocol, "port:", port)
	return saved, needRestart, nil
}

// fillPresetDefaults writes the protocol's settings/streamSettings blobs and a
// seeded client, for the preset named by `preset` — the two VLESS presets
// differ only in that one field, so the blob builders key off it rather than
// guessing from the protocol column. It only sets the fields that actually
// reach xray: Settings, StreamSettings, Protocol, Port, Listen and Enable.
// Everything else is the panel's own bookkeeping and AddInbound normalizes it.
func (s *InboundService) fillPresetDefaults(in *model.Inbound, preset InboundPreset) {
	switch in.Protocol {
	case model.VLESS:
		if preset.Security == "tls" {
			in.Settings = s.presetVlessSettings(false)
			in.StreamSettings = s.presetTlsStream()
		} else {
			in.Settings = s.presetVlessSettings(true)
			in.StreamSettings = s.presetRealityStream()
		}
	case model.Hysteria2:
		// Hysteria2 runs its own QUIC transport: the stream network is "hysteria"
		// (what genHysteriaLink and the stream model both key on) with the
		// protocol's own settings block, over TLS with h3 ALPN. A plain tcp/none
		// stream made the link a dead inbound.
		in.Settings = s.presetHysteriaSettings()
		in.StreamSettings = s.presetHysteriaStream()
	case model.TUIC:
		// TUIC carries its own QUIC transport ("tuic" network + tuicSettings),
		// not UDP-with-reality: REALITY covers only TCP-layer camouflage and the
		// stream model pins TUIC's transport itself.
		in.Settings = s.presetTuicSettings()
		in.StreamSettings = s.presetTuicStream()
	case model.NAIVE:
		// NaiveProxy IS a TLS protocol (an HTTPS masquerade): security must be
		// tls, and the settings block names which wires it owns.
		in.Settings = s.presetNaiveSettings()
		in.StreamSettings = s.presetNaiveStream()
	case model.Shadowsocks:
		// The settings block is the 2022 method + user table, not a bare
		// clients array: the 2022 key is an INBOUND credential and the users
		// each carry their own PSK for that method.
		in.Settings = s.presetShadowsocksSettings()
		in.StreamSettings = `{"network":"tcp","security":"none"}`
	}
}

// presetVlessSettings builds the account blob for a VLESS preset. The vision
// argument keeps the client's XTLS Vision flow: only meaningful under REALITY
// over TCP; a TLS preset leaves the flow blank and the operator picks it in
// the inbound's form (the flow needs a TCP+TLS transport, which is what the
// TLS preset is).
func (s *InboundService) presetVlessSettings(vision bool) string {
	uid := s.generateRandomCredential(model.VLESS)
	flow := ""
	if vision {
		flow = "xtls-rprx-vision"
	}
	clients := []map[string]any{
		{
			"email": presetAccountEmail(), "enable": true,
			"id": uid, "flow": flow, "encryption": "none",
		},
	}
	return presetMarshal(map[string]any{"clients": clients})
}

func (s *InboundService) presetHysteriaSettings() string {
	auth := s.generateRandomCredential(model.Hysteria2)
	clients := []map[string]any{
		{"email": presetAccountEmail(), "enable": true, "auth": auth},
	}
	// version:2 selects the Hysteria2 protocol the operator asked for; the
	// panel's settings model keys it there, not in the protocol column.
	return presetMarshal(map[string]any{"version": 2, "clients": clients})
}

func (s *InboundService) presetTuicSettings() string {
	uid := s.generateRandomCredential(model.TUIC)
	// TUIC authenticates with BOTH a uuid (the identity) and a password; a
	// uuid-only entry would not log in. The password is the credential for
	// the uuid, not a second account.
	clients := []map[string]any{
		{"email": presetAccountEmail(), "enable": true, "id": uid, "password": s.generateRandomCredential(model.TUIC)},
	}
	// bbr is the preset's whole point; the congestion control lives on the
	// settings block, not per client.
	return presetMarshal(map[string]any{"congestionControl": "bbr", "clients": clients})
}

func (s *InboundService) presetTuicStream() string {
	// TUIC's transport is its own QUIC block; TLS carries the operator's
	// certificate (empty here: the form's Security section binds one) with h3,
	// which genTuicLink's comment pins as mandatory.
	return `{"network":"tuic","security":"tls","tlsSettings":{"minVersion":"1.3","maxVersion":"1.3","alpn":["h3"],"certificates":[],"settings":{"fingerprint":"chrome"}}}`
}

func (s *InboundService) presetNaiveSettings() string {
	// Naive's settings block names which wires the listener owns: tcp = HTTP/2
	// over TLS, udp = HTTP/3 over QUIC, both on one port is the default. The
	// masquerade answers probes with a 404 page, which is the whole point of
	// the protocol.
	clients := []map[string]any{
		{"email": presetAccountEmail(), "enable": true, "password": s.generateRandomCredential(model.NAIVE)},
	}
	return presetMarshal(map[string]any{
		"clients":    clients,
		"network":    "tcp",
		"masquerade": map[string]any{"type": "404", "file": "", "url": "", "string": ""},
	})
}

func (s *InboundService) presetNaiveStream() string {
	// NaiveProxy IS a TLS protocol: an HTTPS masquerade the client dials. TLS
	// with an empty certificate is the deliberate starting point — the
	// operator binds a cert from the Security section (or points SNI at a CDN
	// front and disables it) before clients connect.
	return `{"network":"tcp","security":"tls","tlsSettings":{"minVersion":"1.2","maxVersion":"1.3","alpn":["h2","http/1.1"],"certificates":[],"settings":{"fingerprint":"chrome"}}}`
}

func (s *InboundService) presetShadowsocksSettings() string {
	// The 2022 family: an inbound-level 2022 key for the server and a per-user
	// PSK for each account. 2022-blake3-aes-128-gcm takes a 16-byte key
	// (base64-32 chars), which is what the user key minter below emits.
	key, err := s.preset2022Key(16)
	if err != nil {
		logger.Warning("Shadowsocks preset 2022 key generation failed; using static key:", err)
		key = "AAAAAAAABBBBAAAACCCCCC=="
	}
	clients := []map[string]any{
		{"email": presetAccountEmail(), "enable": true, "password": preset2022KeyB64(16)},
	}
	return presetMarshal(map[string]any{
		"method": "2022-blake3-aes-128-gcm",
		"key":    key,
		"network": "tcp",
		"clients": clients,
	})
}

// presetAccountEmail is the seeded account's email: short, stable and
// recognizable without colliding with operator-named clients.
func presetAccountEmail() string {
	return fmt.Sprintf("preset-%d", time.Now().UnixNano()%100000000)
}

// presetMarshal builds the inbound's settings blob: a clients array with
// whatever keys the protocol's entry needs, plus any protocol-level defaults
// the preset carries. This is the EXACT shape AddInboundClient expects to
// splice into the inbound, so a preset and a typed inbound are byte-equivalent
// when re-saved through the UI.
func presetMarshal(extra map[string]any) string {
	blob := map[string]any{"clients": presetClients(extra)}
	for k, v := range extra {
		blob[k] = v
	}
	b, _ := json.Marshal(blob)
	return string(b)
}

// presetClients pulls the client row the preset seeded, defaulting to an
// empty table when the caller did not supply one.
func presetClients(extra map[string]any) []map[string]any {
	if c, ok := extra["clients"].([]map[string]any); ok {
		return c
	}
	return []map[string]any{}
}

// presetX25519Pair mints a fresh x25519 key pair the way the panel's own
// REALITY generator does, via the xray binary. The panel's ssl service owns
// the "random keys" button on the inbound's edit form; this is the same
// derivation, called from the preset path.
func (s *InboundService) presetX25519Pair() (string, string, error) {
	// The panel exposes x25519 generation through ServerService.GetNewX25519Cert
	// — but that lives on a different service, so the preset goes through
	// the shared xray binary path instead: one exec, two base64 lines back.
	out, err := execXraySubcommand("x25519")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("xray x25519: expected 2 lines, got %d", len(lines))
	}
	priv := strings.TrimSpace(strings.SplitN(lines[0], ":", 2)[1])
	pub := strings.TrimSpace(strings.SplitN(lines[1], ":", 2)[1])
	return priv, pub, nil
}

// execXraySubcommand runs a subcommand on the bundled xray binary and returns
// its stdout. Isolated so a preset's crypto path has one obvious place to
// swap when xray moves.
func execXraySubcommand(args ...string) (string, error) {
	cmd := exec.Command(xray.GetBinaryPath(), args...)
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// presetInboundPort picks a free port the way the panel picks one anywhere
// else: scan the inbounds table for taken ports, then ask the kernel whether
// the candidate is actually bindable. Two back-to-back presets land on
// different ports because the scan walks a band with a random start.
func (s *InboundService) presetInboundPort() (int, error) {
	used := map[int]bool{}
	var ports []int
	if err := database.GetDB().Model(&model.Inbound{}).Pluck("port", &ports).Error; err == nil {
		for _, p := range ports {
			used[p] = true
		}
	}
	// 20001 + random start, step 7 across a 40000-wide band.
	start := 20001 + int(rand.Intn(39999))
	for i := 0; i < 40000; i++ {
		port := start + i*7
		if port > 60000 {
			port = 20001 + (port % 40000)
		}
		if used[port] {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port found for the preset inbound")
}

// presetTlsStream is the streamSettings blob for a VLESS+TLS preset. An empty
// certificate is deliberate: the operator binds one from the Security section
// (or points SNI at a CDN front that terminates TLS) before clients connect.
// serverName is left empty on purpose: a TLS preset's SNI is whatever the
// operator names — a domain, or the CDN front's — and a wrong one fails the
// handshake. The form's Identity → Listen IP field is where it belongs.
func (s *InboundService) presetTlsStream() string {
	return `{"network":"tcp","security":"tls","tlsSettings":{"minVersion":"1.2","maxVersion":"1.3","alpn":["h2","http/1.1"],"certificates":[],"settings":{"fingerprint":"chrome"},"serverName":""}}`
}

// presetRealityStream is the streamSettings blob for a VLESS+REALITY preset.
// Multi-SNI camouflage to Cloudflare so DPI has no easy fingerprint; the
// operator can re-pick SNI from the inbound's edit form later. The blob keeps
// the panel's own shape: publicKey and fingerprint ride in the settings
// sub-object, the way the form's RealityStreamSettings serializes them.
func (s *InboundService) presetRealityStream() string {
	priv, pub, err := s.presetX25519Pair()
	if err != nil {
		logger.Warning("REALITY preset x25519 generation failed; using static pair:", err)
		priv, pub = "0000000000000000000000000000000000000000000000000000000000000000",
			"0000000000000000000000000000000000000000000000000000000000000000"
	}
	return fmt.Sprintf(
		`{"network":"tcp","security":"reality","realitySettings":{"dest":"1.1.1.1:443","serverNames":["www.microsoft.com","www.google.com","cloudflare.com","www.bing.com","apple.com"],"privateKey":"%s","shortIds":["","3a7bd3f2"],"settings":{"publicKey":"%s","fingerprint":"chrome","spiderX":"/"}}}`,
		priv, pub,
	)
}

// presetHysteriaStream is the streamSettings blob for a Hysteria2 preset:
// the protocol's own QUIC transport with its settings block, over TLS pinned
// to h3 (which genHysteriaLink's comment marks mandatory). An empty
// certificate is the deliberate starting point, bound from the Security
// section before use.
func (s *InboundService) presetHysteriaStream() string {
	return `{"network":"hysteria","security":"tls","hysteriaSettings":{"protocol":"hysteria","version":2,"udpIdleTimeout":60},"tlsSettings":{"minVersion":"1.3","maxVersion":"1.3","alpn":["h3"],"certificates":[],"settings":{"fingerprint":"chrome"}}}`
}

// ApplyPresetOverrides takes a freshly created preset inbound and rewrites
// the operator's options onto it: SNI (tlsSettings serverName or the reality
// target), a CDN front (an externalProxy entry the client dials instead of
// the server), and a port the operator picked over the panel's random one.
// An empty value is a no-op. The rewrite persists through the panel's own
// update path, so a port collision or a rejected stream comes back as the
// caller's error rather than a silently ignored override.
func (s *InboundService) ApplyPresetOverrides(inbound *model.Inbound, preset InboundPreset, sni, cdn string, port int) (*model.Inbound, error) {
	changed := false

	// SNI: where it lands depends on the stream layer the preset built.
	// Each helper mutates the blob only; the single UpdateInbound at the
	// end persists all of it at once.
	if sni != "" {
		if err := s.applyPresetSni(inbound, preset, sni); err != nil {
			logger.Warning("preset SNI override: ", err)
		} else {
			changed = true
		}
	}

	// CDN front: a second endpoint the client dials, forwarding to the inbound.
	if cdn != "" {
		if err := s.applyPresetCdn(inbound, cdn); err != nil {
			logger.Warning("preset CDN override: ", err)
		} else {
			changed = true
		}
	}

	// Port: a specific listen port over the panel's random pick.
	if port > 0 && port != inbound.Port {
		inbound.Port = port
		changed = true
	}

	if !changed {
		return inbound, nil
	}
	// One update persists the SNI/CDN blob rewrite and the port together; the
	// panel's own path re-checks the port (excluding this row) and validates
	// the stream.
	updated, _, err := s.UpdateInbound(inbound)
	if err != nil {
		return inbound, err
	}
	*inbound = *updated
	return inbound, nil
}

// applyPresetSni writes the operator's SNI onto the preset's stream settings:
// a tls layer gets tlsSettings.serverName; a reality layer gets the target
// and serverNames both pointing at it.
func (s *InboundService) applyPresetSni(inbound *model.Inbound, preset InboundPreset, sni string) error {
	var stream map[string]any
	if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
		return err
	}
	if preset.Security == "reality" {
		reality, ok := stream["realitySettings"].(map[string]any)
		if !ok {
			reality = map[string]any{}
			stream["realitySettings"] = reality
		}
		reality["dest"] = sni + ":443"
		reality["serverNames"] = sni
	} else if tls, ok := stream["tlsSettings"].(map[string]any); ok {
		tls["serverName"] = sni
	}
	bs, err := json.Marshal(stream)
	if err != nil {
		return err
	}
	inbound.StreamSettings = string(bs)
	return nil
}

// applyPresetCdn adds an externalProxy entry the client dials instead of the
// server's own address — the CDN front that terminates TLS and forwards. The
// entry shape is the panel's own (the form's external proxy row): {forceTls,
// dest, port, remark}.
func (s *InboundService) applyPresetCdn(inbound *model.Inbound, cdn string) error {
	host, portStr := cdn, "443"
	if i := strings.LastIndex(cdn, ":"); i > 0 {
		host, portStr = cdn[:i], cdn[i+1:]
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("preset CDN front: bad port in %q", cdn)
	}
	var stream map[string]any
	if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
		return err
	}
	proxies, _ := stream["externalProxy"].([]any)
	proxies = append(proxies, map[string]any{
		"forceTls": "same",
		"dest":     host,
		"port":     port,
		"remark":   "cdn",
	})
	stream["externalProxy"] = proxies
	bs, err := json.Marshal(stream)
	if err != nil {
		return err
	}
	inbound.StreamSettings = string(bs)
	return nil
}

// preset2022Key mints a 2022 PSK of the cipher's key length, base64-encoded:
// the panel's own user-key shape for the 2022-blake3 family, where a hex or
// arbitrary string is refused by the cipher.
func (s *InboundService) preset2022Key(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// preset2022KeyB64 is the no-error form of the minter above, for a key the
// caller will never refuse: 2022-blake3-aes-128-gcm's 16-byte PSK.
func preset2022KeyB64(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}
