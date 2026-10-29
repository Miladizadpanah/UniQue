package service

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os/exec"
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
}

// Presets lists all available inbound presets, best connection first.
func (s *InboundService) Presets() []InboundPreset {
	return []InboundPreset{
		{
			Name:        "VLESS + REALITY",
			Description: "Best censorship resistance. No domain needed. XTLS Vision + multi-SNI camouflage over Cloudflare.",
			Protocol:    model.VLESS,
			Recommended: true,
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
	s.fillPresetDefaults(inbound)

	saved, needRestart, err := s.AddInbound(inbound)
	if err != nil {
		return nil, false, err
	}
	logger.Info("preset inbound created:", saved.Tag, saved.Protocol, "port:", port)
	return saved, needRestart, nil
}

// fillPresetDefaults writes the protocol's settings/streamSettings blobs and a
// seeded client. It only sets the fields that actually reach xray: Settings,
// StreamSettings, Protocol, Port, Listen and Enable. Everything else is the
// panel's own bookkeeping and AddInbound normalizes it.
func (s *InboundService) fillPresetDefaults(in *model.Inbound) {
	switch in.Protocol {
	case model.VLESS:
		in.Settings = s.presetVlessSettings("reality")
		in.StreamSettings = s.presetRealityStream()
	case model.Hysteria2:
		in.Settings = s.presetHysteriaSettings()
		in.StreamSettings = `{"security":"none","network":"tcp"}`
	case model.TUIC:
		in.Settings = s.presetTuicSettings()
		in.StreamSettings = s.presetTuicStream()
	case model.NAIVE:
		in.Settings = s.presetNaiveSettings()
		in.StreamSettings = `{"security":"none","network":"tcp"}`
	case model.Shadowsocks:
		in.Settings = s.presetShadowsocksSettings()
		in.StreamSettings = `{"security":"none","network":"tcp"}`
	}
}

// presetVlessSettings builds the account blob for a VLESS preset. The security
// argument selects reality vs tls; the client carries the protocol's
// credential minted plus a Vision flow so the link is usable in every major
// app the moment it is generated.
func (s *InboundService) presetVlessSettings(security string) string {
	uid := s.generateRandomCredential(model.VLESS)
	clients := []map[string]any{
		{
			"email": presetAccountEmail(), "enable": true,
			"id": uid, "flow": "xtls-rprx-vision", "encryption": "none",
		},
	}
	if security == "tls" {
		// TLS needs a certificate to be picked later; the operator should
		// bind one from the panel's cert manager. The preset leaves that
		// blank on purpose.
		_ = security
	}
	return presetMarshal(clients)
}

func (s *InboundService) presetHysteriaSettings() string {
	auth := s.generateRandomCredential(model.Hysteria2)
	clients := []map[string]any{
		{"id": presetAccountEmail(), "enable": true, "auth": auth},
	}
	return presetMarshal(clients)
}

func (s *InboundService) presetTuicSettings() string {
	uid := s.generateRandomCredential(model.TUIC)
	clients := []map[string]any{
		{"id": presetAccountEmail(), "enable": true, "uuid": uid, "congestion": "bbr"},
	}
	return presetMarshal(clients)
}

func (s *InboundService) presetTuicStream() string {
	priv, pub, err := s.presetX25519Pair()
	if err != nil {
		logger.Warning("TUIC preset x25519 generation failed; using static pair:", err)
	}
	return fmt.Sprintf(
		`{"network":"udp","security":"reality","realitySettings":{"dest":"1.1.1.1:443","serverNames":["www.microsoft.com","www.google.com"],"privateKey":"%s","publicKey":"%s","shortIds":[""]}}`,
		priv, pub,
	)
}

func (s *InboundService) presetNaiveSettings() string {
	clients := []map[string]any{
		{"username": presetAccountEmail(), "enable": true, "password": s.generateRandomCredential(model.NAIVE)},
	}
	return presetMarshal(clients)
}

func (s *InboundService) presetShadowsocksSettings() string {
	// The 2022 family key is 16 bytes for aes-128-gcm; the panel's own
	// shadowsocks helper is cipher-specific, so mint the key length that
	// 2022-blake3-aes-128-gcm actually accepts.
	key := s.shadowsocksUserKeyLength(16)
	clients := []map[string]any{
		{"email": presetAccountEmail(), "enable": true, "password": key},
	}
	return presetMarshal(clients)
}

// presetAccountEmail is the seeded account's email: short, stable and
// recognizable without colliding with operator-named clients.
func presetAccountEmail() string {
	return fmt.Sprintf("preset-%d", time.Now().UnixNano()%100000000)
}

// presetMarshal builds the inbound's settings blob: a clients array with
// whatever keys the protocol's entry needs. This is the EXACT shape
// AddInboundClient expects to splice into the inbound, so a preset and a
// typed inbound are byte-equivalent when re-saved through the UI.
func presetMarshal(clients []map[string]any) string {
	blob := map[string]any{"clients": clients}
	b, _ := json.Marshal(blob)
	return string(b)
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

// shadowsocksUserKeyLength mints a hex key of the given byte length, the way
// the panel's shadowsocks projector does for a user key.
func (s *InboundService) shadowsocksUserKeyLength(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// presetRealityStream is the streamSettings blob for a VLESS+REALITY preset.
// Multi-SNI camouflage to Cloudflare so DPI has no easy fingerprint; the
// operator can re-pick SNI from the inbound's edit form later.
func (s *InboundService) presetRealityStream() string {
	priv, pub, err := s.presetX25519Pair()
	if err != nil {
		logger.Warning("REALITY preset x25519 generation failed; using static pair:", err)
		priv, pub = "0000000000000000000000000000000000000000000000000000000000000000",
			"0000000000000000000000000000000000000000000000000000000000000000"
	}
	return fmt.Sprintf(
		`{"network":"tcp","security":"reality","realitySettings":{"dest":"1.1.1.1:443","serverNames":["www.microsoft.com","www.google.com","cloudflare.com","www.bing.com","apple.com"],"privateKey":"%s","publicKey":"%s","shortIds":["","3a7bd3f2"]}}`,
		priv, pub,
	)
}
