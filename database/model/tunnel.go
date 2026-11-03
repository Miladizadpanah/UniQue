package model

import (
	"database/sql/driver"
	"encoding/json"
	"strconv"
)

// WebhookDelivery is one webhook POST attempt: what was sent, where it went,
// and whether the receiving endpoint accepted it. The outbox table is bounded
// (the retention pruner keeps the most recent N per URL); a failed delivery
// that the operator has not retried yet still shows up in the log so they can
// see WHY their client did not get the notification.
type WebhookDelivery struct {
	Id       int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Url      string `json:"url" gorm:"index"`
	EventType string `json:"eventType" gorm:"index"`
	Source   string `json:"source"`
	// Status is "ok" when the receiving endpoint answered <400, or the
	// receiver's own error string otherwise. A retry of the same event gets
	// a fresh row (the table is an append-only log, not a queue), so the
	// operator's view is "N attempts, the last one said M".
	Status    string `json:"status" gorm:"type:text;default:''"`
	ResponseCode int  `json:"responseCode" gorm:"default:0"`
	// At is the UnixMilli of the attempt itself.
	At int64 `json:"at" gorm:"index"`
}

// TableName pins the table name: "webhook_deliveries".
func (WebhookDelivery) TableName() string { return "webhook_deliveries" }

// WebhookDeliveryRetention is how many delivery attempts per URL the
// retention pruner keeps. 500 attempts covers roughly two weeks of 30s
// cadence (500 * 30s = 4.17h); a longer window costs nothing until the
// operator actually runs hundreds of webhook URLs, which is not the case
// yet. The pruner drops the oldest rows beyond the cap per URL.
const WebhookDeliveryRetention = 500

// UserSession is one active session the panel is aware of: who logged in,
// where they logged in from, when, and whether they have been revoked. The
// panel's session store is a cookie (gin-contrib/sessions), which it cannot
// enumerate server-side, so the active-sessions surface is a SHADOW LOG:
// the login path records each session here, and the revoke path marks the
// row revoked. The next request from a revoked session loads its user row,
// sees the flag, and logs out. The log is bounded (the retention pruner
// keeps the most recent N per user, and prunes revoked rows older than the
// max session age).
type UserSession struct {
	Id      int    `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId  int    `json:"userId" gorm:"index"`
	// Fingerprint is the session's identifying token: a random 32-hex value
	// minted at login and stored in the session cookie's session store
	// (a separate key from the user id), so a session the operator revokes
	// cannot be identified by the cookie alone (which every request from
	// the same browser carries).
	Fingerprint string `json:"fingerprint" gorm:"uniqueIndex"`
	Ip          string `json:"ip" gorm:"index"`
	UserAgent   string `json:"userAgent" gorm:"type:text;default:''"`
	// CreatedAt/RevokedAt are UnixMilli; 0 means never revoked.
	CreatedAt int64 `json:"createdAt" gorm:"default:0"`
	RevokedAt int64 `json:"revokedAt" gorm:"default:0"`
}

// TableName pins the table name: "user_sessions".
func (UserSession) TableName() string { return "user_sessions" }

// ApiToken is one scoped API credential the panel's operator mints: a
// random 64-hex secret, a scope bitmask (which classes of the node API the
// holder may use), and an optional expiry. The shared nodeApiToken (the
// settings-table value the status endpoint has always read) remains the
// "full access" credential; an ApiToken row is the granular version, the
// same shape as the admin's own permission bitmask, so the operator can
// hand a hub a READ-ONLY token for monitoring and a separate token that
// may also provision, without the read-only holder being able to write.
//
// The gate (web/controller/node_api.go) accepts EITHER credential: the
// shared node token (full access, the legacy path) or one of the panel's
// own scoped tokens (the scope restricts the routes it may open). The two
// paths coexist, and a panel that has never minted a scoped token behaves
// exactly as before — the shared token is the only answer, and it is still
// the default.
type ApiToken struct {
	Id     int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Name   string `json:"name" gorm:"not null"`            // the operator's label, e.g. "hub-nl-1"
	Secret string `json:"secret" gorm:"uniqueIndex"`       // 64 hex chars, the credential
	// Scope is the capability bitmask, the same reading as model.Permission's
	// position: a scope of 0 is "no scoped routes at all" (the token still
	// answers the shared-token path it rides on, so 0 is "read-only on the
	// shared token's own read routes", not "no access").
	Scope     int64  `json:"scope" gorm:"default:0"`
	ExpiresAt int64  `json:"expiresAt" gorm:"default:0"` // UnixMilli, 0 = no expiry
	Remark    string `json:"remark" gorm:"default:''"`
	CreatedAt int64  `json:"createdAt" gorm:"default:0"`
}

// TableName pins the table name: "api_tokens".
func (ApiToken) TableName() string { return "api_tokens" }

// The scope bits, the same class as model.Permission (a capability bitmask
// read on every request by the gate). Appended, never inserted: the values
// are positional, so inserting a bit would shift every scope already stored
// in the database by one.
const (
	// ScopeNodeRead opens the node API's read routes: /inbounds, /clients,
	// /pairing/status, /mtls/ca. The hub's "show me what this node serves"
	// case, no write capability.
	ScopeNodeRead int64 = 1 << iota
	// ScopeNodeWrite opens the node API's write routes: /inbounds (create),
	// /clients, /delClient, /purge, the mTLS sign route, and the pairing
	// request. The "hub provisions accounts on this node" case.
	ScopeNodeWrite
	// ScopeHeartbeat opens the node API's telemetry route: /heartbeat, the
	// push side of the hub<->node 30s beat. A token that only carries this
	// bit is the "this panel reports its gauges" credential — no read, no
	// write, just the telemetry channel.
	ScopeHeartbeat
)

// AllApiTokenScopes is the canonical list, in the order the UI renders it.
var AllApiTokenScopes = []struct {
	Bit  int64
	Slug string
}{
	{ScopeNodeRead, "nodeRead"},
	{ScopeNodeWrite, "nodeWrite"},
	{ScopeHeartbeat, "heartbeat"},
}

// NodeMetrics is one sampled gauge reading the hub records against a node's row
// when it learns the gauges: push beats, pull polls, and its own local
// heartbeat. The table is purely additive — an old binary ignores it — and the
// retention policy (drop readings older than the retention window) keeps it
// bounded: a 30s beat pruned at 24h is 288 rows per node, a 60s poll at 24h
// is 144; both are trivial next to the inbounds table they summarize.
type NodeMetrics struct {
	Id      int    `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeId  int    `json:"nodeId" gorm:"index"`
	Source  string `json:"source" gorm:"index"` // "heartbeat" | "poll" | "local"
	CpuPct  float64 `json:"cpuPct"`
	MemPct  float64 `json:"memPct"`
	NetUp   uint64 `json:"netUp"`
	NetDown uint64 `json:"netDown"`
	Ts      int64  `json:"ts" gorm:"index"` // UnixMilli
}

// TableName pins the table name: "node_metrics".
func (NodeMetrics) TableName() string { return "node_metrics" }

// TunnelHealthEvent is one health transition of a tunnel row: the moment a
// tunnel moved between states (up -> stalled, stalled -> up, ...) or failed
// a link-test, the watchdog records a row here. The table is a bounded log
// (pruned to the most recent N per tunnel by the retention job), not a
// history table — it answers "when did this tunnel last stall, and what did
// the engine say" rather than "show me every transition since January".
//
// The event's State is the NEW state (the one the row now carries); the
// detail field is the engine's own reason (the LastError the watchdog copied
// into the row on the transition, or a link-test latency measurement). An
// empty detail means "no reason recorded" (a clean up, say), which the UI
// renders as just the state flip.
type TunnelHealthEvent struct {
	Id       int    `json:"id" gorm:"primaryKey;autoIncrement"`
	TunnelId int    `json:"tunnelId" gorm:"index"`
	// State is the NEW state the tunnel row carries after the event.
	State string `json:"state"`
	// Detail is the engine's reason for the transition: the LastError text on
	// a failure, a latency measurement on a link-test, or "" on a clean flip.
	Detail string `json:"detail" gorm:"type:text;default:''"`
	// Ts is the UnixMilli of the transition itself.
	Ts int64 `json:"ts" gorm:"index"`
}

// TableName pins the table name: "tunnel_health_events".
func (TunnelHealthEvent) TableName() string { return "tunnel_health_events" }

// TunnelHealthRetention is how many health events per tunnel the retention
// job keeps. 200 events covers a full week of 30s watchdog beats (7 * 24 *
// 120 = 20,160 beats, but the retention is PER TUNNEL, so a 200-row cap means
// a 200-row window, roughly 16h of stalls at the 30s cadence). A shorter
// cap still covers the operator's "what just happened" question; a longer cap
// costs nothing until the operator actually runs hundreds of tunnels, which
// is not the case yet.
const TunnelHealthRetention = 200

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

// Value implements driver.Valuer so GORM can persist a TunnelAddr column as a
// JSON string. Without this, a plain struct field fails at the DB layer with
// "unsupported type model.TunnelAddr, a struct" the moment a row is created.
func (a TunnelAddr) Value() (driver.Value, error) {
	return json.Marshal(a)
}

// Scan implements sql.Scanner: the inverse of Value, so a stored JSON string
// rehydrates into the struct. A blank/absent value leaves the zero TunnelAddr,
// which the service layer treats as "no address set".
func (a *TunnelAddr) Scan(v any) error {
	if v == nil {
		*a = TunnelAddr{}
		return nil
	}
	var raw []byte
	switch t := v.(type) {
	case []byte:
		raw = t
	case string:
		raw = []byte(t)
	default:
		// A value we don't know how to parse is left empty rather than
		// failing the whole row: a bad cell is a no-address, not a crash.
		*a = TunnelAddr{}
		return nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		*a = TunnelAddr{}
		return nil
	}
	return json.Unmarshal(raw, a)
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
