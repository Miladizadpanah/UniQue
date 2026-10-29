package service

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"golang.org/x/crypto/curve25519"
)

// InboundPreset is a ready-to-use inbound template with optimized defaults.
type InboundPreset struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Protocol    model.Protocol `json:"protocol"`
	Network     string         `json:"network"`
	Security    string         `json:"security"`
	Recommended bool           `json:"recommended"`
}

// Presets lists all available inbound presets.
func (s *InboundService) Presets() []InboundPreset {
	return []InboundPreset{
		{
			Name:        "VLESS + REALITY",
			Description: "Best censorship resistance. No domain needed.",
			Protocol:    model.VLESS,
			Network:     "tcp",
			Security:    "reality",
			Recommended: true,
		},
		{
			Name:        "VLESS + TLS",
			Description: "Standard TLS with domain. Works well with CDN.",
			Protocol:    model.VLESS,
			Network:     "tcp",
			Security:    "tls",
			Recommended: false,
		},
		{
			Name:        "Hysteria2",
			Description: "High-speed QUIC-based protocol. Best for mobile networks.",
			Protocol:    model.Hysteria2,
			Network:     "kcp",
			Security:    "none",
			Recommended: true,
		},
		{
			Name:        "TUIC v5",
			Description: "Next-gen UDP protocol. Excellent performance.",
			Protocol:    model.TUIC,
			Network:     "tcp",
			Security:    "reality",
			Recommended: false,
		},
		{
			Name:        "NaiveProxy",
			Description: "Simple HTTP/HTTPS obfuscation. Good for restrictive networks.",
			Protocol:    model.NAIVE,
			Network:     "tcp",
			Security:    "none",
			Recommended: false,
		},
	}
}

// PresetInbound creates a new inbound from a preset with optimized defaults.
func (s *InboundService) PresetInbound(preset InboundPreset, remark string) (*model.Inbound, error) {
	// Pick a free port
	port, err := s.presetInboundPort()
	if err != nil {
		return nil, err
	}

	// Build the settings JSON
	settings, streamSettings := s.buildPresetSettings(preset, port)

	inbound := &model.Inbound{
		Tag:            fmt.Sprintf("preset-%s-%d", preset.Protocol, port),
		Protocol:       preset.Protocol,
		Listen:         "0.0.0.0",
		Port:           port,
		Enable:         true,
		Remark:         strings.TrimSpace(remark),
		Settings:       settings,
		StreamSettings: streamSettings,
	}

	// Save to database
	db := database.GetDB()
	if err := db.Create(inbound).Error; err != nil {
		return nil, err
	}

	logger.Info("Preset inbound created:", inbound.Tag, "port:", port)
	return inbound, nil
}

// buildPresetSettings generates optimized JSON settings for each protocol.
func (s *InboundService) buildPresetSettings(preset InboundPreset, port int) (string, string) {
	var settings, streamSettings string

	switch preset.Protocol {
	case model.VLESS:
		if preset.Security == "reality" {
			settings, streamSettings = s.buildRealityPreset(port)
		} else {
			settings, streamSettings = s.buildTlsPreset(port)
		}
	case model.Hysteria, model.Hysteria2:
		settings, streamSettings = s.buildHysteriaPreset(port)
	case model.TUIC:
		settings, streamSettings = s.buildTuicPreset(port)
	case model.NAIVE:
		settings, streamSettings = s.buildNaivePreset(port)
	case model.Shadowsocks:
		settings, streamSettings = s.buildShadowsocksPreset(port)
	default:
		// Fallback: simple client
		clients := []map[string]any{
			{"email": "seed", "enable": true, "id": generateUUID()},
		}
		settings = toJSON(clients)
		streamSettings = toJSON(map[string]any{"security": "none"})
	}

	return settings, streamSettings
}

// buildRealityPreset creates VLESS/VMess with REALITY optimized for best connection.
func (s *InboundService) buildRealityPreset(port int) (string, string) {
	// Generate x25519 key pair
	pubKey, privKey := s.generateX25519Keys()

	// Cloudflare IPs for maximum resistance
	dest := "1.1.1.1:443"

	// Best SNI targets that are hard to block
	serverNames := []string{
		"www.microsoft.com",
		"www.google.com",
		"cloudflare.com",
		"apple.com",
		"yahoo.com",
	}

	stream := map[string]any{
		"network":  "tcp",
		"security": "reality",
		"realitySettings": map[string]any{
			"dest":         dest,
			"serverNames":  serverNames,
			"privateKey":   privKey,
			"publicKey":    pubKey,
			"shortIds":     []string{"00000000", "6a5b3f9c"},
			"minClientVer": "",
			"maxClientVer": "",
		},
	}

	// Create main client with Vision flow
	clients := []map[string]any{
		{"email": "main", "enable": true, "id": generateUUID(), "flow": "xtls-rprx-vision", "encryption": "none"},
	}

	return toJSON(clients), toJSON(stream)
}

// buildTlsPreset creates VLESS/VMess with TLS.
func (s *InboundService) buildTlsPreset(port int) (string, string) {
	stream := map[string]any{
		"network":  "tcp",
		"security": "tls",
		"tlsSettings": map[string]any{
			"alpn": []string{"h3", "http/1.1"},
		},
	}

	clients := []map[string]any{
		{"email": "seed", "enable": true, "id": generateUUID(), "flow": "xtls-rprx-vision", "encryption": "none"},
	}

	return toJSON(clients), toJSON(stream)
}

// buildHysteriaPreset creates Hysteria2 with BBR.
func (s *InboundService) buildHysteriaPreset(port int) (string, string) {
	// Hysteria2 prefers UDP
	stream := map[string]any{
		"network":  "kcp",
		"security": "none",
	}

	// Generate auth token
	auth := generateAuthToken(16)

	clients := []map[string]any{
		{"id": "user1", "enable": true, "auth": auth, "level": 0, "up": "100 mbit", "down": "100 mbit"},
	}

	settings := map[string]any{
		"clients": clients,
		"hibp":    false,
		"obfs":    map[string]any{"type": "sb"},
	}

	return toJSON(settings), toJSON(stream)
}

// buildTuicPreset creates TUIC v5 with REALITY and BBR congestion control.
func (s *InboundService) buildTuicPreset(port int) (string, string) {
	pubKey, privKey := s.generateX25519Keys()
	clientID := generateUUID()
	secret := generateAuthToken(16)

	// Use Cloudflare for resistance
	serverNames := []string{"www.microsoft.com", "www.google.com"}

	stream := map[string]any{
		"network":  "tcp",
		"security": "reality",
		"realitySettings": map[string]any{
			"dest":        "1.1.1.1:443",
			"serverNames": serverNames,
			"privateKey":  privKey,
			"publicKey":   pubKey,
			"shortIds":    []string{"00000000", "6a5b3f9c"},
		},
	}

	settings := map[string]any{
		"clients": []map[string]any{
			{"id": clientID, "enable": true, "secret": secret, "level": 0, "udpMode": 0, "congestion": "bbr"},
		},
		"udpRelayMode": "native",
	}

	return toJSON(settings), toJSON(stream)
}

// buildNaivePreset creates NaiveProxy.
func (s *InboundService) buildNaivePreset(port int) (string, string) {
	stream := map[string]any{
		"network":  "tcp",
		"security": "none",
	}

	settings := map[string]any{
		"clients": []map[string]any{
			{"username": "user1", "enable": true, "password": generateAuthToken(12)},
		},
	}

	return toJSON(settings), toJSON(stream)
}

// buildShadowsocksPreset creates Shadowsocks with 2022-blake3.
func (s *InboundService) buildShadowsocksPreset(port int) (string, string) {
	stream := map[string]any{
		"network":  "tcp",
		"security": "none",
	}

	settings := map[string]any{
		"method":  "2022-blake3-aes-128-gcm",
		"network": "tcp",
		"clients": []map[string]any{
			{"id": generateUUID(), "enable": true, "password": generateAuthToken(16)},
		},
	}

	return toJSON(settings), toJSON(stream)
}

// Helper functions

// generateUUID creates a random UUID.
func generateUUID() string {
	uuid := make([]byte, 16)
	rand.Read(uuid)
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
}

// generateAuthToken creates a random base64-encoded token of the given length.
func generateAuthToken(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// generateX25519Keys generates a x25519 key pair for REALITY.
func (s *InboundService) generateX25519Keys() (string, string) {
	// Generate private key
	priv := make([]byte, 32)
	rand.Read(priv)

	// Compute public key
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		logger.Warning("X25519 key generation failed:", err)
		return "", ""
	}

	// Base64 encode both
	// Return as hex strings
	return fmt.Sprintf("%x", pub), fmt.Sprintf("%x", priv)
}

// toJSON marshals to JSON string.
func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// presetInboundPort finds an available port.
func (s *InboundService) presetInboundPort() (int, error) {
	for port := 40000; port < 60000; port += 10 {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			// Check if port is already in use
			var count int64
			database.GetDB().Model(&model.Inbound{}).Where("port = ?", port).Count(&count)
			if count == 0 {
				return port, nil
			}
		}
	}
	return 0, fmt.Errorf("no free port found")
}
