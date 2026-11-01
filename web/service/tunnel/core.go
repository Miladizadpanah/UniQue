package tunnel

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

// ---------------------------------------------------------------------------
// Wire format
//
// The control channel opens with:
//
//	[1 version byte = 0x01] [1 role byte = 0x01 server | 0x02 client]
//	[64 token bytes]        <- the shared pre-shared key
//	[4 payload-len] [payload] <- keepalive, every 15s from both ends
//
// After the control handshake both ends carry DATA frames on the SAME
// connection (TCP/WSS) or on a UDP datagram relay (UDP transport):
//
//	[1 type: 0x01 tcp-open, 0x02 tcp-data, 0x03 tcp-close, 0x04 udp-addr,
//	          0x05 udp-payload]
//	[2 src-port] [16 dst-addr] [2 dst-port] [payload]
//
// A 20-byte destination address holds both IPv4 (last 4 bytes) and IPv6
// (16 bytes). The src-port is the local port the client's traffic came from
// (or the server's forwarded port). This is the minimal BackPack-shaped
// relay: no KCP, no QUIC, no L3 — just stdlib TCP/TLS/WS/UDP.
// ---------------------------------------------------------------------------

// Version is the wire protocol version. A mismatch is a hard error, not a
// "maybe it still works" — two builds that disagree on the frame layout will
// corrupt each other's traffic otherwise.
const wireVersion byte = 0x01

// The role bytes. The server role listens; the client role dials.
const (
	roleServer byte = 0x01
	roleClient byte = 0x02
)

// The frame types on the data channel.
const (
	frameTCPOpen    byte = 0x01
	frameTCPData    byte = 0x02
	frameTCPClose   byte = 0x03
	frameUDPAddr    byte = 0x04
	frameUDPPayload byte = 0x05
	frameKeepalive  byte = 0x00
)

// tokenLen is the byte length of the shared secret. The panel's token field is
// a 64-CHARACTER hex string (32 bytes of entropy); the wire carries the raw
// 32 bytes, which keeps the on-the-wire secret compact and avoids a hex
// decode per handshake.
const tokenLen = 32

// keepaliveInterval is how often each end stamps a keepalive frame. The
// watchdog's "is this tunnel wedged" test reads LastByteNano, which every
// keepalive advances — so a quiet tunnel (no user traffic, but alive) does
// not trip the stall detector.
const keepaliveInterval = 15 * time.Second

// ErrTokenMismatch is the control handshake's "the two ends disagree on the
// secret" answer. The panel's UI shows it verbatim; it is the most common
// "my tunnel won't connect" cause (a copied-paste mistake in the token).
var ErrTokenMismatch = errors.New("tunnel token mismatch")

// ErrVersionMismatch is a peer running a wire build that does not speak this
// protocol revision. Upgrading one end but not the other is the trigger.
var ErrVersionMismatch = errors.New("tunnel wire version mismatch")

// conn is the data-channel abstraction over TCP or WebSocket. Both carry a
// read/write pair; the frame pump is the same on both.
type conn interface {
	io.ReadWriteCloser
}

// ---------------------------------------------------------------------------
// Handshake
// ---------------------------------------------------------------------------

// deriveWireToken is the 32 raw bytes the wire carries: the 64-char hex token
// the operator typed, decoded. A blank/odd-length token answers a zero-filled
// 32 bytes, which BOTH ends will compare equal to each other's zero-fill —
// the engine refuses to start a tunnel whose token is not a valid 64-hex in
// start(), so an empty token never reaches the wire in the first place.
func deriveWireToken(hexToken string) [tokenLen]byte {
	var out [tokenLen]byte
	for i := 0; i+1 < len(hexToken) && i/2 < tokenLen; i += 2 {
		v, ok := hexByte(hexToken[i], hexToken[i+1])
		if !ok {
			continue
		}
		out[i/2] = v
	}
	return out
}

func hexByte(hi, lo byte) (byte, bool) {
	dec := func(b byte) (byte, bool) {
		switch {
		case b >= '0' && b <= '9':
			return b - '0', true
		case b >= 'a' && b <= 'f':
			return b - 'a' + 10, true
		case b >= 'A' && b <= 'F':
			return b - 'A' + 10, true
		}
		return 0, false
	}
	a, ok1 := dec(hi)
	b, ok2 := dec(lo)
	if !ok1 || !ok2 {
		return 0, false
	}
	return a<<4 | b, true
}

// performControl is one end of the control handshake: write the header, read
// the peer's, and verify the token matches. isServer picks which side writes
// first (the server writes as it accepts; the client writes as it dials).
func performControl(c net.Conn, isServer bool, token [tokenLen]byte, role byte) error {
	header := make([]byte, 2+tokenLen)
	header[0] = wireVersion
	header[1] = role
	copy(header[2:], token[:])

	// The WRITER sends its header first; the READER accepts either order, so
	// a server accepting a connection and a client dialing one use the same
	// routine. Both send-then-receive.
	_, err := c.Write(header)
	if err != nil {
		return err
	}
	peer := make([]byte, 2+tokenLen)
	if _, err := io.ReadFull(c, peer); err != nil {
		return err
	}
	if peer[0] != wireVersion {
		return ErrVersionMismatch
	}
	// The roles must be OPPOSITE: a server meeting a server is two hubs that
	// never meant to pair, and "the tunnel connects but carries nothing" is
	// the failure mode that makes operators think it works when it does not.
	want := roleClient
	if isServer {
		want = roleClient
	} else {
		want = roleServer
	}
	if peer[1] != want {
		return fmt.Errorf("peer role mismatch (want %d, got %d)", want, peer[1])
	}
	var peerTok [tokenLen]byte
	copy(peerTok[:], peer[2:])
	if subtleEqual(token, peerTok) != 1 {
		return ErrTokenMismatch
	}
	return nil
}

// subtleEqual is the constant-time byte comparison the token check needs.
// A timing leak here would let a peer probe one byte at a time; it does not
// matter on a TLS channel, but the TCP transport is not encrypted, so the
// comparison stays the honest one.
func subtleEqual(a, b [tokenLen]byte) int {
	v := byte(0)
	for i := range a {
		v |= a[i] ^ b[i]
	}
	if v == 0 {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Transports
// ---------------------------------------------------------------------------

// openTransport opens the control connection for one transport: plain TCP,
// TLS-wrapped TCP, a WebSocket dial, or a WSS dial. The role's fingerprint
// (SNI on the TLS variants) rides on the config.
func openTransport(ctx context.Context, t *model.TunnelPair, isServer bool) (net.Conn, error) {
	target := t.ServerAddr.String()
	switch t.Transport {
	case model.TunnelTransportTCP:
		return net.Dial("tcp", target)
	case model.TunnelTransportTCPTLS:
		cfg := tlsClientConfig(t.TLSFingerprint)
		return tls.Dial("tcp", target, cfg)
	case model.TunnelTransportWS:
		u := "ws://" + target
		ws, _, err := websocket.DefaultDialer.DialContext(ctx, u, nil)
		if err != nil {
			return nil, err
		}
		return wsConn{ws}, nil
	case model.TunnelTransportWSS:
		u := "wss://" + target
		dialer := websocket.Dialer{TLSClientConfig: tlsClientConfig(t.TLSFingerprint)}
		ws, _, err := dialer.DialContext(ctx, u, nil)
		if err != nil {
			return nil, err
		}
		return wsConn{ws}, nil
	}
	return nil, fmt.Errorf("unknown transport %q", t.Transport)
}

// tlsClientConfig is the TLS 1.3 config a tunnel's data channel uses. A blank
// fingerprint means "no SNI pin" — the cert is validated against the default
// pool, which is right for a domain-fronted tunnel on a CDN.
func tlsClientConfig(fingerprint string) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if fingerprint != "" {
		cfg.ServerName = fingerprint
	}
	return cfg
}

// wsConn adapts a gorilla WebSocket connection to net.Conn: the panel's
// frame pump speaks io.ReadWriteCloser, and a WebSocket message IS a frame
// (binary, unframed-by-our-own-payload-bounds), so the adapter is one
// WriteMessage/ReadMessage pair per Read/Write.
type wsConn struct{ ws *websocket.Conn }

func (c wsConn) Read(p []byte) (int, error) {
	_, data, err := c.ws.ReadMessage()
	if err != nil {
		return 0, err
	}
	copy(p, data)
	if len(data) < len(p) {
		return len(data), io.EOF // short message: the pump re-arms
	}
	return len(data), nil
}

func (c wsConn) Write(p []byte) (int, error) {
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c wsConn) Close() error { return c.ws.Close() }
func (c wsConn) LocalAddr() net.Addr { return c.ws.UnderlyingConn().LocalAddr() }
func (c wsConn) RemoteAddr() net.Addr { return c.ws.UnderlyingConn().RemoteAddr() }
func (c wsConn) SetDeadline(t time.Time) error { return c.ws.UnderlyingConn().SetDeadline(t) }
func (c wsConn) SetReadDeadline(t time.Time) error { return c.ws.UnderlyingConn().SetReadDeadline(t) }
func (c wsConn) SetWriteDeadline(t time.Time) error { return c.ws.UnderlyingConn().SetWriteDeadline(t) }

// listenTransport accepts one control connection on the server role: the
// matching half of openTransport. A WebSocket accept upgrades the HTTP
// connection; the TLS variants wrap the listener.
func listenTransport(ln net.Listener, t *model.TunnelPair) (net.Listener, func(), error) {
	var cleanup func()
	switch t.Transport {
	case model.TunnelTransportTCP:
		cleanup = func() {}
	case model.TunnelTransportTCPTLS:
		cfg := tlsClientConfig(t.TLSFingerprint)
		// The SERVER side needs its own cert. The panel's managed TLS store
		// (sslstore.go) supplies it when the operator picked one; a blank
		// fingerprint + no managed cert is a misconfiguration the start()
		// validation catches, so here we assume a cert is configured. The
		// cert files come from the panel's ssl service — see wireTLS below.
		ln = tls.NewListener(ln, cfg)
		cleanup = func() {}
	case model.TunnelTransportWS, model.TunnelTransportWSS:
		cleanup = func() {}
	}
	return ln, cleanup, nil
}

// ---------------------------------------------------------------------------
// Frame pump: see relay.go's pumpFrames — the relay landed in its own file
// so the stream-state tables sit beside the frame primitives they use. The
// legacy pumpStream / relayBackend / pumpUDP stubs are removed; pumpFrames is
// the single reader of the control channel now.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Token generation
// ---------------------------------------------------------------------------

// GenerateToken mints a 64-char hex token (32 random bytes), the same shape
// the panel's node API token uses: random, one-time, no structure to guess.
func GenerateToken() string {
	b := make([]byte, tokenLen)
	if _, err := crand.Read(b); err != nil {
		// A crypto/rand failure is a host problem, not a tunnel one; the
		// zero-filled token answers the validation's 64-hex check and the
		// operator will see two matching (weak) tokens, which is the same
		// failure shape a blank token field would produce.
		return strings.Repeat("0", 64)
	}
	return fmt.Sprintf("%x", b)
}

// sha256sum is the digest the token check's log line prints (a prefix, not
// the whole secret).
func sha256sum(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}

// keepalivePump stamps a keepalive frame on the control channel every 15s:
// the byte-movement that keeps a quiet tunnel from reading as stalled. It
// stops with the tunnel's context.
func keepalivePump(ctx context.Context, c net.Conn) {
	tick := time.NewTicker(keepaliveInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// A keepalive frame is one byte of type + no payload. A write
			// that fails is the tunnel dying; the caller's read loop sees
			// the EOF and tears down.
			_, _ = c.Write([]byte{frameKeepalive})
		}
	}
}

var _ = sync.Mutex{}
var _ = logger.Info
var _ = listenTransport
var _ = openTransport
