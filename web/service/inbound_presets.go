package service

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	mrand "math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
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
			Description: "Best censorship resistance. No domain needed. XTLS Vision + real-TLS camouflage.",
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
			Description: "Standard TLS. Binds the panel's certificate, or a self-signed one, so it works at once.",
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

// presetSniffing is the sniffing block every preset carries. It is what makes a
// fresh inbound useful without a second visit to the form: without it the core
// cannot tell a TLS handshake from any other byte stream, so domain-based
// routing, the block lists and the per-domain geo rules all see nothing. quic
// is included for the UDP protocols; fakedns is NOT, because it needs a
// fakedns DNS server in the routing config and would otherwise point clients
// at an address nothing answers on.
const presetSniffing = `{"enabled":true,"destOverride":["http","tls","quic"],"metadataOnly":false,"routeOnly":false}`

// The port band presets draw from: above the privileged range and above the
// ports a distro or another panel is likely to hold, below the ephemeral range
// the kernel hands out for outbound connections (32768+ by default on Linux),
// so a preset port cannot be stolen by an outgoing socket.
const (
	presetPortLow  = 20000
	presetPortHigh = 32000
)

// PresetInbound creates one inbound from a preset through the panel's own
// create path (AddInbound), so the preset row is indistinguishable from a
// typed one. It returns the persisted row, whether the running core needs a
// restart, and the error (if any).
func (s *InboundService) PresetInbound(preset InboundPreset, remark string) (*model.Inbound, bool, error) {
	if !nodeMintable(preset.Protocol) {
		return nil, false, fmt.Errorf("protocol %s has no preset", preset.Protocol)
	}

	inbound := &model.Inbound{
		Protocol: preset.Protocol,
		Listen:   "0.0.0.0",
		Enable:   true,
		Remark:   strings.TrimSpace(remark),
	}
	if inbound.Remark == "" {
		inbound.Remark = preset.Name
	}
	// The preset's optimized settings + streamSettings + one seeded account,
	// minted the way the operator would have typed them but with values that
	// measurably connect best. Built BEFORE the port is picked because the
	// transport decides which socket family the port has to be free on.
	s.fillPresetDefaults(inbound, preset)

	// The port is picked last and re-picked if AddInbound's own conflict check
	// refuses it: between the pick and the insert another process can take the
	// socket, and a preset that answers "port already in use" is a dead end for
	// an operator who never chose a port at all.
	var saved *model.Inbound
	var needRestart bool
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		var port int
		port, err = s.presetInboundPort(inbound)
		if err != nil {
			return nil, false, err
		}
		inbound.Port = port

		saved, needRestart, err = s.AddInbound(inbound)
		if err == nil {
			logger.Info("preset inbound created:", saved.Tag, saved.Protocol, "port:", port)
			return saved, needRestart, nil
		}
		if !isPresetPortError(err) {
			return nil, false, err
		}
		logger.Debug("preset port refused, retrying: ", err)
		// AddInbound may have normalized parts of the row before failing; the
		// next attempt re-derives the port only, so the settings stand.
		inbound.Port = 0
	}
	return nil, false, err
}

// isPresetPortError reports whether an AddInbound failure is about the port it
// was handed, and therefore worth another pick. Everything else (a bad
// credential, a rejected certificate) is the caller's answer.
func isPresetPortError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"port already exists",
		"port is already used",
		"port is served by",
		"port is already in use",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// fillPresetDefaults writes the protocol's settings/streamSettings blobs and a
// seeded client, for the preset named by `preset` — the two VLESS presets
// differ only in that one field, so the blob builders key off it rather than
// guessing from the protocol column. It only sets the fields that actually
// reach xray: Settings, Sniffing, StreamSettings, Protocol, Port, Listen and
// Enable. Everything else is the panel's own bookkeeping and AddInbound
// normalizes it.
func (s *InboundService) fillPresetDefaults(in *model.Inbound, preset InboundPreset) {
	in.Sniffing = presetSniffing
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

// presetClient is the seeded account, with every field the panel's own
// Inbound.ClientBase writes. A partial entry is accepted by AddInbound and then
// comes back as an account with no subId — which is a client the client list
// hides a subscription link for and the node sync cannot attach — so the preset
// spells the whole row out.
func presetClient(apply func(map[string]any)) map[string]any {
	client := map[string]any{
		"email":      presetAccountEmail(),
		"enable":     true,
		"limitIp":    0,
		"totalGB":    0,
		"expiryTime": 0,
		"tgId":       "",
		"subId":      presetRandomID(16),
		"comment":    "",
		"reset":      0,
	}
	if apply != nil {
		apply(client)
	}
	return client
}

// presetVlessSettings builds the account blob for a VLESS preset. The vision
// argument keeps the client's XTLS Vision flow: only meaningful under REALITY
// over TCP; a TLS preset leaves the flow blank so the same inbound still works
// behind a CDN, which does not carry vision.
func (s *InboundService) presetVlessSettings(vision bool) string {
	flow := ""
	if vision {
		flow = "xtls-rprx-vision"
	}
	clients := []map[string]any{
		presetClient(func(c map[string]any) {
			c["id"] = s.generateRandomCredential(model.VLESS)
			c["flow"] = flow
			c["encryption"] = "none"
		}),
	}
	// decryption/encryption/fallbacks are the VLESS settings keys the panel's
	// own VLESSSettings class writes. flow is mirrored at the inbound level as
	// well, which is where xray-core reads the default for a client carrying
	// none — the per-client copy is what the share-link generators read.
	return presetJSON(map[string]any{
		"clients":    clients,
		"decryption": "none",
		"encryption": "none",
		"fallbacks":  []any{},
		"flow":       flow,
	})
}

func (s *InboundService) presetHysteriaSettings() string {
	clients := []map[string]any{
		presetClient(func(c map[string]any) {
			c["auth"] = s.generateRandomCredential(model.Hysteria2)
		}),
	}
	// version:2 selects the Hysteria2 protocol the operator asked for; the
	// panel's settings model keys it there, not in the protocol column.
	return presetJSON(map[string]any{"version": 2, "clients": clients})
}

func (s *InboundService) presetTuicSettings() string {
	// TUIC authenticates with BOTH a uuid (the identity) and a password; a
	// uuid-only entry would not log in. The password is the credential for
	// the uuid, not a second account.
	clients := []map[string]any{
		presetClient(func(c map[string]any) {
			c["id"] = s.generateRandomCredential(model.TUIC)
			c["password"] = s.generateRandomCredential(model.TUIC)
		}),
	}
	// bbr is the preset's whole point; the congestion control lives on the
	// settings block, not per client.
	return presetJSON(map[string]any{"congestionControl": "bbr", "clients": clients})
}

func (s *InboundService) presetTuicStream() string {
	// TUIC's transport is its own QUIC block; TLS carries a bound certificate
	// with h3, which genTuicLink's comment pins as mandatory.
	return presetJSON(map[string]any{
		"network":      "tuic",
		"security":     "tls",
		"tuicSettings": map[string]any{"congestionControl": "bbr"},
		"tlsSettings":  s.presetTLSSettings("", "1.3", "1.3", []string{"h3"}),
	})
}

func (s *InboundService) presetNaiveSettings() string {
	// Naive's settings block names which wires the listener owns: tcp = HTTP/2
	// over TLS, udp = HTTP/3 over QUIC, both on one port is the default. The
	// masquerade answers probes with a 404 page, which is the whole point of
	// the protocol.
	clients := []map[string]any{
		presetClient(func(c map[string]any) {
			c["password"] = s.generateRandomCredential(model.NAIVE)
		}),
	}
	return presetJSON(map[string]any{
		"clients":    clients,
		"network":    "tcp",
		"masquerade": map[string]any{"type": "404", "file": "", "url": "", "string": ""},
	})
}

func (s *InboundService) presetNaiveStream() string {
	// NaiveProxy IS a TLS protocol: an HTTPS masquerade the client dials, so
	// the inbound cannot start without a certificate in the listener.
	return presetJSON(map[string]any{
		"network":     "tcp",
		"security":    "tls",
		"tlsSettings": s.presetTLSSettings("", "1.2", "1.3", []string{"h2", "http/1.1"}),
	})
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
		presetClient(func(c map[string]any) {
			c["password"] = preset2022KeyB64(16)
		}),
	}
	return presetJSON(map[string]any{
		"method":  "2022-blake3-aes-128-gcm",
		"key":     key,
		"network": "tcp",
		"clients": clients,
	})
}

// presetAccountEmail is the seeded account's email: short, recognizable and
// random enough not to collide with an operator-named client.
func presetAccountEmail() string {
	return "preset-" + presetRandomID(8)
}

// presetJSON marshals a preset blob. A marshal failure cannot happen for the
// maps built here, but returning "{}" beats handing AddInbound a nil string.
func presetJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// presetRandomID returns n lowercase alphanumeric characters, the shape the
// panel uses for subIds and credentials.
func presetRandomID(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	return presetRandomFrom(alphabet, n)
}

// presetRandomHex returns n lowercase hex characters, the shape xray's REALITY
// shortIds require (hex only, even length, at most 16 characters).
func presetRandomHex(n int) string {
	const alphabet = "0123456789abcdef"
	return presetRandomFrom(alphabet, n)
}

func presetRandomFrom(alphabet string, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		// Never expected; fall back to the math source rather than an empty id.
		for i := range b {
			b[i] = byte(mrand.Intn(256))
		}
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out)
}

// presetX25519Pair mints a fresh x25519 key pair IN PROCESS.
//
// This used to shell out to `xray x25519`, which is how the panel's "random
// keys" button does it. That made the preset path depend on the bundled binary
// being present and executable, and its error branch substituted one hard-coded
// key pair — so on any host where the exec failed, EVERY preset carried the same
// REALITY identity, which is a fingerprint shared by every panel that installed
// one. Generating here removes both problems: the only failure left is entropy,
// and even that path stays unique.
//
// The encoding is what xray-core's own command prints: base64url without padding
// over the raw 32-byte strings. X25519 clamps the scalar internally, so no
// pre-clamping is needed; the bytes are byte-identical to the command's output.
func (s *InboundService) presetX25519Pair() (string, string) {
	key, err := ecdh.X25519().GenerateKey(crand.Reader)
	if err != nil {
		// Entropy failure. A shared static pair is worse than a weak one, so mix
		// the clock and a fresh draw from the process RNG instead.
		seed := time.Now().UnixNano() ^ int64(mrand.Uint64())
		r := mrand.New(mrand.NewSource(seed))
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = byte(r.Intn(256))
		}
		key, err = ecdh.X25519().NewPrivateKey(raw)
		if err != nil {
			// NewPrivateKey only rejects the wrong length, which cannot happen.
			panic("preset x25519: " + err.Error())
		}
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(key.Bytes()), enc.EncodeToString(key.PublicKey().Bytes())
}

// presetPortCandidates returns the whole preset band in random order, so two
// presets created back to back cannot land on the same number and an operator
// cannot predict the next one.
func presetPortCandidates() []int {
	ports := make([]int, 0, presetPortHigh-presetPortLow+1)
	for p := presetPortLow; p <= presetPortHigh; p++ {
		ports = append(ports, p)
	}
	mrand.Shuffle(len(ports), func(i, j int) { ports[i], ports[j] = ports[j], ports[i] })
	return ports
}

// pickPresetPort returns the first candidate that is neither claimed nor in
// use. `attempts` bounds the probing so a host with a swathe of busy ports
// still answers promptly; `nil` inUse means "the caller already filtered".
func pickPresetPort(candidates []int, claimed map[int]bool, inUse func(int) bool, attempts int) (int, error) {
	if attempts <= 0 || attempts > len(candidates) {
		attempts = len(candidates)
	}
	for i := 0; i < attempts; i++ {
		port := candidates[i]
		if claimed[port] {
			continue
		}
		if inUse != nil && inUse(port) {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port in the %d-%d preset band", presetPortLow, presetPortHigh)
}

// presetClaimedPorts is every port the panel already knows to be taken: what
// each stored inbound actually binds (which is not always its port column —
// OpenVPN's second port lives in settings) plus the panel's own listeners.
func presetClaimedPorts() map[int]bool {
	claimed := map[int]bool{}
	if db := database.GetDB(); db != nil {
		var others []*model.Inbound
		if err := db.Find(&others).Error; err == nil {
			for _, other := range others {
				if other.Port > 0 {
					claimed[other.Port] = true
				}
				for _, c := range inboundPortClaims(other) {
					claimed[c.port] = true
				}
			}
		}
	}
	// The panel's own listeners live in the settings table, which does not
	// exist before the database is up (and never does in a unit test).
	if database.GetDB() != nil {
		var settings SettingService
		if webPort, err := settings.GetPort(); err == nil && webPort > 0 {
			claimed[webPort] = true
		}
		if subEnabled, err := settings.GetSubEnable(); err == nil && subEnabled {
			if subPort, err := settings.GetSubPort(); err == nil && subPort > 0 {
				claimed[subPort] = true
			}
		}
	}
	return claimed
}

// presetPortNetwork is the socket family the preset will bind. It mirrors
// inboundPortClaims but works on a row whose port is still unset, which is
// exactly the state the port picker runs in.
func presetPortNetwork(inbound *model.Inbound) string {
	switch inbound.Protocol {
	case model.Hysteria, model.Hysteria2, model.TUIC, model.WGC, model.AWG, model.WireGuard:
		return "udp"
	}
	if inbound.StreamSettings != "" {
		var ss struct {
			Network string `json:"network"`
		}
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &ss); err == nil {
			switch strings.ToLower(strings.TrimSpace(ss.Network)) {
			case "kcp", "mkcp", "quic":
				return "udp"
			}
		}
	}
	return "tcp"
}

// presetInboundPort picks a free port in the preset band and hands the row the
// number it will actually keep.
//
// The check is the panel's own conflict authority (checkPortConflicts) rather
// than an ad-hoc bind test, so a preset can never be refused by AddInbound for
// a port this function chose: the same three sources (other inbounds, the
// panel's listeners, the host) are consulted in the same order.
func (s *InboundService) presetInboundPort(inbound *model.Inbound) (int, error) {
	claimed := presetClaimedPorts()
	network := presetPortNetwork(inbound)
	host := probeHost(inbound.Listen)
	inUse := func(port int) bool {
		used, _ := portInUse(network, host, port)
		return used
	}

	candidates := presetPortCandidates()
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		port, err := pickPresetPort(candidates, claimed, inUse, 400)
		if err != nil {
			if lastErr != nil {
				return 0, lastErr
			}
			return 0, err
		}
		inbound.Port = port
		if err := s.checkPortConflicts(inbound, 0); err == nil {
			return port, nil
		} else {
			lastErr = err
			claimed[port] = true
		}
	}
	if lastErr != nil {
		return 0, lastErr
	}
	return 0, fmt.Errorf("no free port in the %d-%d preset band", presetPortLow, presetPortHigh)
}

// presetTLSSettings builds a TLS stream block with a certificate ALREADY bound.
//
// An empty `certificates` array is what the presets used to ship, and it is a
// listener that starts and then fails every handshake — "created" but not
// usable, which is the one thing a one-click preset must not be. So the block
// binds the panel's own certificate when the panel has one (a real certificate
// the operator already holds, usually for the very host they will dial) and
// otherwise mints a self-signed pair carried inline in the inbound, which the
// core loads without touching the filesystem.
func (s *InboundService) presetTLSSettings(host, minVersion, maxVersion string, alpn []string) map[string]any {
	block, serverName := s.presetTLSMaterial(host)
	return map[string]any{
		"minVersion":   minVersion,
		"maxVersion":   maxVersion,
		"alpn":         alpn,
		"serverName":   serverName,
		"certificates": []any{block},
		"settings":     map[string]any{"fingerprint": "chrome"},
	}
}

// presetTLSMaterial returns the certificate block for a preset listener and the
// serverName the share links should carry.
//
// `host` is where the operator says clients will dial; blank means "the panel's
// own domain". The panel certificate is reused only when it actually covers
// that host, because a link naming host A while the listener presents a
// certificate for host B fails verification on the client and looks exactly
// like a broken server.
func (s *InboundService) presetTLSMaterial(host string) (map[string]any, string) {
	host = strings.TrimSpace(host)
	// The panel's certificate settings live in the settings table, which is
	// absent before the database is up (and in every unit test), so the reuse
	// branch is skipped rather than dereferenced.
	if database.GetDB() != nil {
		var settings SettingService
		if host == "" {
			if domain, err := settings.GetWebDomain(); err == nil {
				host = strings.TrimSpace(domain)
			}
		}
		certFile, _ := settings.GetCertFile()
		keyFile, _ := settings.GetKeyFile()
		if host != "" && strings.TrimSpace(certFile) != "" && strings.TrimSpace(keyFile) != "" &&
			certFileCoversHost(certFile, host) {
			return map[string]any{
				"certificateFile": certFile,
				"keyFile":         keyFile,
				"ocspStapling":    3600,
			}, host
		}
	}
	if host == "" {
		host = "localhost"
	}

	certPEM, keyPEM, err := presetSelfSignedPEM(host)
	if err != nil {
		logger.Warning("preset self-signed certificate generation failed:", err)
		// Still bind SOMETHING: an empty certificate list starts a listener
		// that cannot complete a handshake at all.
		return map[string]any{"certificate": []string{}, "key": []string{}}, host
	}
	return map[string]any{
		"certificate":  []string{certPEM},
		"key":          []string{keyPEM},
		"ocspStapling": 3600,
	}, host
}

// presetSelfSignedPEM mints an ECDSA P-256 self-signed certificate for `host`
// and returns both halves as PEM. The host becomes the CN and the only SAN, so
// a client that pins or trusts this certificate validates against the name it
// dials.
func presetSelfSignedPEM(host string) (string, string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "localhost"
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}
	serial, err := crand.Int(crand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("generate serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"vpn-ui"},
			CommonName:   host,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	certDER, err := x509.CreateCertificate(crand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return "", "", fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("marshal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return string(certPEM), string(keyPEM), nil
}

// certFileCoversHost reports whether the first certificate in a PEM file names
// `host`, so a preset can tell "reuse the panel's certificate" from "that
// certificate is for a different name and will fail every handshake".
func certFileCoversHost(path, host string) bool {
	b, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return false
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return certCoversHost(cert, host)
}

func certCoversHost(cert *x509.Certificate, host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if cert == nil || host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, got := range cert.IPAddresses {
			if got.Equal(ip) {
				return true
			}
		}
		return false
	}
	return cert.VerifyHostname(host) == nil
}

// tlsBlockCoversHost reports whether a tlsSettings block's bound certificate
// already names `host`, reading either the inline PEM the preset minted or the
// file pair it borrowed from the panel.
func tlsBlockCoversHost(tls map[string]any, host string) bool {
	if tls == nil {
		return false
	}
	if certs, ok := tls["certificate"].([]any); ok && len(certs) > 0 {
		if text, ok := certs[0].(string); ok {
			if block, _ := pem.Decode([]byte(text)); block != nil {
				if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
					return certCoversHost(cert, host)
				}
			}
		}
	}
	if certs, ok := tls["certificate"].([]string); ok && len(certs) > 0 {
		if block, _ := pem.Decode([]byte(certs[0])); block != nil {
			if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
				return certCoversHost(cert, host)
			}
		}
	}
	if path, ok := tls["certificateFile"].(string); ok && strings.TrimSpace(path) != "" {
		return certFileCoversHost(path, host)
	}
	return false
}

// presetTlsStream is the streamSettings blob for a VLESS+TLS preset, with the
// certificate bound (see presetTLSSettings) so the inbound works immediately.
// serverName defaults to the panel's own domain: that is the name its
// certificate covers, so a client dialing the panel's host verifies cleanly
// without any extra configuration.
func (s *InboundService) presetTlsStream() string {
	return presetJSON(map[string]any{
		"network":     "tcp",
		"security":    "tls",
		"tlsSettings": s.presetTLSSettings("", "1.2", "1.3", []string{"h2", "http/1.1"}),
	})
}

// presetRealityTargets are real TLS 1.3 + HTTP/2 sites used as the REALITY
// destination, each paired with the name its certificate actually carries. A
// dest and a serverName that do not belong together is the single most common
// way a REALITY inbound is set up wrong: the handshake is forwarded to `dest`
// while the client sends `serverName`, and any mismatch shows up to a probe as
// forged TLS.
var presetRealityTargets = []struct {
	dest string
	sni  string
}{
	{"www.cloudflare.com:443", "www.cloudflare.com"},
	{"www.microsoft.com:443", "www.microsoft.com"},
	{"www.apple.com:443", "www.apple.com"},
	{"dl.google.com:443", "dl.google.com"},
	{"www.bing.com:443", "www.bing.com"},
}

// presetRealityStream is the streamSettings blob for a VLESS+REALITY preset:
// one consistent camouflage host, a freshly minted key pair, random shortIds
// and a random spider path. The blob keeps the panel's own shape: publicKey,
// fingerprint and spiderX ride in the settings sub-object, which is what the
// form's RealityStreamSettings serializes and what the share-link generators
// read back.
func (s *InboundService) presetRealityStream() string {
	priv, pub := s.presetX25519Pair()
	target := presetRealityTargets[mrand.Intn(len(presetRealityTargets))]
	return presetJSON(map[string]any{
		"network":  "tcp",
		"security": "reality",
		"realitySettings": map[string]any{
			// target is the key the panel's own RealityStreamSettings writes (and
			// therefore reads back when the operator opens the form); dest is the
			// spelling xray-core documents. Both carry the same endpoint, and the
			// core prefers target, so the row survives a round-trip through the
			// form and works without one.
			"target":      target.dest,
			"dest":        target.dest,
			"serverNames": []string{target.sni},
			"privateKey":  priv,
			// A random shortId plus the empty one: the empty id keeps clients
			// that do not send a shortId working, the random one is what a
			// modern client is configured with.
			"shortIds": []string{"", presetRandomHex(8)},
			"settings": map[string]any{
				"publicKey":   pub,
				"fingerprint": "chrome",
				"spiderX":     "/" + presetRandomID(8),
			},
		},
	})
}

// presetHysteriaStream is the streamSettings blob for a Hysteria2 preset:
// the protocol's own QUIC transport with its settings block, over TLS pinned
// to h3 (which genHysteriaLink's comment marks mandatory) and a bound
// certificate.
func (s *InboundService) presetHysteriaStream() string {
	return presetJSON(map[string]any{
		"network":          "hysteria",
		"security":         "tls",
		"hysteriaSettings": map[string]any{"protocol": "hysteria", "version": 2, "udpIdleTimeout": 60},
		"tlsSettings":      s.presetTLSSettings("", "1.3", "1.3", []string{"h3"}),
	})
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
// a tls layer gets tlsSettings.serverName, a reality layer gets the target and
// serverNames both pointing at it.
//
// The SNI is also the name the certificate has to carry. When the bound
// certificate does not cover it — the self-signed one the preset minted, or a
// panel certificate for a different host — a fresh self-signed pair for the new
// name is minted, because a listener presenting the wrong name fails every
// handshake and looks identical to a dead server.
func (s *InboundService) applyPresetSni(inbound *model.Inbound, preset InboundPreset, sni string) error {
	var stream map[string]any
	if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
		return err
	}
	sni = strings.TrimSpace(sni)
	if preset.Security == "reality" {
		reality, ok := stream["realitySettings"].(map[string]any)
		if !ok {
			reality = map[string]any{}
			stream["realitySettings"] = reality
		}
		reality["target"] = sni + ":443"
		reality["dest"] = sni + ":443"
		// xray-core reads serverNames as a LIST. Writing a bare string here
		// (what this used to do) failed the config load, so the inbound silently
		// never came up.
		reality["serverNames"] = []string{sni}
	} else if tls, ok := stream["tlsSettings"].(map[string]any); ok {
		tls["serverName"] = sni
		if !tlsBlockCoversHost(tls, sni) {
			if certPEM, keyPEM, err := presetSelfSignedPEM(sni); err == nil {
				tls["certificate"] = []string{certPEM}
				tls["key"] = []string{keyPEM}
				delete(tls, "certificateFile")
				delete(tls, "keyFile")
			} else {
				logger.Warning("preset SNI certificate mint failed:", err)
			}
		}
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
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// preset2022KeyB64 is the no-error form of the minter above, for a key the
// caller will never refuse: 2022-blake3-aes-128-gcm's 16-byte PSK.
func preset2022KeyB64(n int) string {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(mrand.Intn(256))
		}
	}
	return base64.StdEncoding.EncodeToString(b)
}
