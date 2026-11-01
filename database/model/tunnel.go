package model

import (
	"encoding/json"
	"strconv"
)

// ---------------------------------------------------------------------------
// Native tunnel engine (BackPack-style): a panel-owned point-to-point tunnel
// between two panel instances. The server ROLE listens on a port and exposes
// forwarded ports to the local network; the client ROLE dials the server and
// carries local port traffic through it. Both roles run on panels, so the
// whole thing is managed from this panel's own UI — no external binary.
//
// Named TunnelPair (not Tunnel) because this table stores the NEW native
// point-to-point engine; the panel already has a TunnelMesh type (the
// hub/spoke Xray mesh) on the "tunnels" table. The pair's table is
// "tunnel_pairs" so the two never collide.
//
// The transports are stdlib-only (TCP, optional TLS, WebSocket, UDP) so they
// build on every platform the panel does. The heavier carriers KCP/QUIC/Noise/
// ICMP live behind build tags or as follow-up phases; a plain encrypted
// tunnel already covers the "Iran ⇄ abroad" reverse case, which is the point.
// ---------------------------------------------------------------------------

// TunnelRole is which end of a tunnel this panel plays. The operator picks it
// per tunnel: "server" is the side that listens (the Iran box, where the port
// must be reachable), "client" is the side that dials (the abroad box).
type TunnelRole string

const (
	TunnelRoleServer TunnelRole = "server"
	TunnelRoleClient TunnelRole = "client"
)

// TunnelStatus is the live state the engine reports to the dashboard.
type TunnelStatus string

const (
	TunnelUp    TunnelStatus = "up"
	TunnelDown  TunnelStatus = "down"
	TunnelStall TunnelStatus = "stalled"
	TunnelError TunnelStatus = "error"
)

// TunnelTransport names the wire carrier. Every one is available; the UI
// shows which are appropriate for the route (link-test recommends one).
type TunnelTransport string

const (
	TunnelTransportTCP    TunnelTransport = "tcp"
	TunnelTransportTCPTLS TunnelTransport = "tcp-tls" // TLS 1.3 over TCP
	TunnelTransportWS     TunnelTransport = "ws"
	TunnelTransportWSS    TunnelTransport = "wss" // TLS 1.3 over WebSocket
	TunnelTransportUDP    TunnelTransport = "udp"
)

// TunnelAddr is one endpoint of the tunnel: the server's listen host:port or
// the client's dial target. A tunnel may list several (failover).
type TunnelAddr struct {
	Host string `json:"host" form:"host"`
	Port int    `json:"port" form:"port"`
}

// String renders host:port (IPv6 bracketed).
func (a TunnelAddr) String() string {
	if a.Port <= 0 {
		return a.Host
	}
	if a.Host != "" && (a.Host[0] == '[' || containsColon(a.Host)) {
		return "[" + a.Host + "]:" + strconv.Itoa(a.Port)
	}
	if a.Host == "" {
		return ":" + strconv.Itoa(a.Port)
	}
	return a.Host + ":" + strconv.Itoa(a.Port)
}

func containsColon(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return true
		}
	}
	return false
}

// TunnelPortMap is one forwarded port: the server (or local, on the client)
// listens on Local and connects to Backend over the tunnel. UDP forwarding is
// a separate per-map flag so a tunnel can carry TCP-only services.
type TunnelPortMap struct {
	Local   int    `json:"local" form:"local"`
	Backend string `json:"backend" form:"backend"` // host:port behind the tunnel
	UDP     bool   `json:"udp" form:"udp"`
}

// String renders "443" or "443=127.0.0.1:2096" or "443/udp=...".
func (p TunnelPortMap) String() string {
	s := strconv.Itoa(p.Local)
	if p.Backend != "" {
		s += "=" + p.Backend
	}
	if p.UDP {
		s += "/udp"
	}
	return s
}

// TunnelPair is one panel-managed point-to-point tunnel.
type TunnelPair struct {
	Id     int `json:"id" gorm:"primaryKey;autoIncrement"`
	// Name is the operator label, e.g. "nl-1 reverse". Unique per panel.
	Name   string `json:"name" gorm:"not null;uniqueIndex"`
	Remark string `json:"remark" gorm:"default:''"`

	// Role is which end THIS panel plays. A complete tunnel needs two panels
	// running opposite roles with the same token; this row is one half.
	Role TunnelRole `json:"role" gorm:"default:'client'"`
	// Direction: "reverse" (the client dials the server; the default, matches
	// the Iran⇄abroad case where the abroad box initiates) — the only mode in
	// this phase. Kept as a field so "direct" (Iran dials) can be added later.
	Direction string `json:"direction" gorm:"default:'reverse'"`
	// Transport is the active wire carrier.
	Transport TunnelTransport `json:"transport" gorm:"default:'tcp'"`
	// FallbackTransports is an ordered JSON array of transports tried in order
	// when the active one stops settling (a wedge, a filtered path).
	FallbackTransports string `json:"fallbackTransports" gorm:"default:'[]'"`

	// ServerAddr is where the server ROLE listens (or, on a client row, where
	// to dial). Port: the tunnel's own control port.
	ServerAddr TunnelAddr `json:"serverAddr" gorm:"type:json"`
	// ForwardedPorts is the JSON array of TunnelPortMap rows this tunnel
	// exposes/uses.
	ForwardedPorts string `json:"forwardedPorts" gorm:"default:'[]'"`

	// Token is the shared pre-shared key, 64 hex chars, that both ends must
	// hold for the handshake to complete. The server ROLE's operator mints it
	// and hands it to the client ROLE's operator (or both panels are in the
	// same operator's hands, which is the normal case).
	Token string `json:"token" gorm:"default:''"`
	// TLSFingerprint is the SNI/ALPN the client presents on -tls carriers;
	// blank = none. A real site name (e.g. "www.microsoft.com") on a
	// CDN-fronted WSS tunnel is what makes the traffic look like ordinary
	// HTTPS browsing.
	TLSFingerprint string `json:"tlsFingerprint" gorm:"default:''"`
	// Tuning: "balance" | "turbo" | "throughput" — queue sizes and read
	// batching. balance is the default; the preset map is in the service.
	Tuning string `json:"tuning" gorm:"default:'balance'"`

	// ProxyProtoV2 stamps the tunnel's PROXY-protocol v2 header on accepted
	// connections so a service behind the tunnel (xray, sshd...) sees the
	// real client address instead of the tunnel's own.
	ProxyProtoV2 bool `json:"proxyProtoV2" gorm:"default:0"`

	Enable bool `json:"enable" gorm:"default:1"`
	// Status is the engine's last report. The row's LastError carries the
	// reason when Status is error.
	Status    TunnelStatus `json:"status" gorm:"default:'down'"`
	LastError string       `json:"lastError" gorm:"type:text;default:''"`
	// Connections is the current open-connection count the engine reports.
	Connections int `json:"connections" gorm:"default:0"`
	// The engine's live gauges, pushed into the row by the watchdog job:
	// how long the tunnel has stayed in its current up state, and the last
	// byte rates, so the dashboard shows "up 2h · 12 MB/s" without a poll.
	UpSince   int64 `json:"upSince" gorm:"default:0"`
	BytesUp   int64 `json:"bytesUp" gorm:"default:0"`
	BytesDown int64 `json:"bytesDown" gorm:"default:0"`
	// CreatedAt/UpdatedAt in UnixMilli, the panel's own convention.
	CreatedAt int64 `json:"createdAt" gorm:"default:0"`
	UpdatedAt int64 `json:"updatedAt" gorm:"default:0"`
}

// TableName pins the table name: "tunnel_pairs", distinct from the legacy
// hub/spoke mesh's "tunnels" table.
func (TunnelPair) TableName() string { return "tunnel_pairs" }

// ForwardedPortsJSON parses the ForwardedPorts blob; a malformed row degrades
// to an empty list (the engine treats "nothing forwarded" as a valid tunnel,
// it just carries no user traffic yet).
func (t *TunnelPair) ForwardedPortsJSON() ([]TunnelPortMap, error) {
	var out []TunnelPortMap
	if t.ForwardedPorts == "" {
		return out, nil
	}
	err := json.Unmarshal([]byte(t.ForwardedPorts), &out)
	return out, err
}

// ForwardedPortsSet marshals one list into the blob shape ForwardedPortsJSON
// parses: the two halves of the row's own round trip.
func ForwardedPortsSet(maps []TunnelPortMap) string {
	b, err := json.Marshal(maps)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// FallbackJSON parses the FallbackTransports blob; a malformed row degrades to
// an empty list (no fallback chain, the active transport stands alone).
func (t *TunnelPair) FallbackJSON() []string {
	var out []string
	if t.FallbackTransports == "" {
		return out
	}
	_ = json.Unmarshal([]byte(t.FallbackTransports), &out)
	return out
}
