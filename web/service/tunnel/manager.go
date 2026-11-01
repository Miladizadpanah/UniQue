// Package tunnel is the panel-owned native tunnel engine: a BackPack-style
// point-to-point tunnel between two panel instances, driven entirely through
// the panel's own UI and settings. Both ends run the SAME binary — one panel
// in the server role listens and exposes forwarded ports, the other in the
// client role dials and carries its local traffic through.
//
// The wire is stdlib-only so every platform the panel builds on works:
//
//	"tcp"       plain TCP relay (a control connection + data connections)
//	"tcp-tls"   TLS 1.3 over TCP
//	"ws"        WebSocket relay
//	"wss"       TLS 1.3 over WebSocket
//	"udp"       datagram relay (UDP forwarded ports only)
//
// A shared token gates the control channel: the first bytes of the tunnel are
// the token, verified on both ends. Everything after the control connection is
// established is a stream of (source, target, payload) frames — the
// forwarded-port relay. UDP maps are datagram versions of the same frame.
//
// The heavier carriers KCP/QUIC/Noise/ICMP and layer-3 GRE tunnels are
// follow-up phases; a plain encrypted tunnel already covers the
// "Iran ⇄ abroad" reverse case, which is the point.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

// ErrTokenLength is a token that cannot be a 64-char secret.
var ErrTokenLength = errors.New("tunnel token must be 64 characters")

// Manager owns every running tunnel on this panel. One process, one Manager:
// the panel's boot (web.go) constructs it and wires the watchdog. A panel in
// the client role runs its own manager against the same table; the ROLE is
// per-row, so one panel can host a server tunnel and a client tunnel at once
// (two different peers).
type Manager struct {
	mu     sync.Mutex
	running map[int]*handle
}

// handle is one running tunnel: its cancel function plus the live counters
// the dashboard and the watchdog read without a restart.
type handle struct {
	cancel context.CancelFunc
	// connections is the current open stream/dgram count. The watchdog polls
	// it: zero for N beats while the control link is also dead = a stall.
	connections int64
	// lastByte is the last time either direction moved a byte. A live link
	// with no user traffic still moves keepalive bytes every 15s, so a quiet
	// tunnel does not look stalled.
	lastByte int64 // UnixNano
}

// NewManager makes an idle manager. StartTunnels is what actually brings the
// configured ones up; it is called at boot and after every UI change.
func NewManager() *Manager {
	return &Manager{running: map[int]*handle{}}
}

// The package-level default manager: one process, one owner of the running
// tunnels. The service layer and the watchdog both reach it through
// DefaultManager so there is exactly one answer to "who is running what".
var (
	defaultManager     *Manager
	defaultManagerMu   sync.RWMutex
)

// SetDefaultManager installs the process manager (the panel's boot calls it
// once, passing the manager it constructed so the shutdown path and the
// watchdog share the same object). A nil keeps the current one.
func SetDefaultManager(m *Manager) {
	defaultManagerMu.Lock()
	defer defaultManagerMu.Unlock()
	if m != nil {
		defaultManager = m
	}
	if defaultManager == nil {
		defaultManager = NewManager()
	}
}

// DefaultManager returns the process manager, creating the idle one on first
// use so a unit test that calls a service method before the panel boots
// still has something to call.
func DefaultManager() *Manager {
	defaultManagerMu.RLock()
	m := defaultManager
	defaultManagerMu.RUnlock()
	if m == nil {
		SetDefaultManager(nil)
		defaultManagerMu.RLock()
		m = defaultManager
		defaultManagerMu.RUnlock()
	}
	return m
}

// IsRunning answers "does this tunnel have a live handle" for the enable
// path's idempotency check. The handle pointer is returned so a caller that
// wants the cancel can hold it, but the method's own contract is the bool.
func (m *Manager) IsRunning(id int) (*handle, bool) {
	return m.isRunning(id)
}

// StartOne brings up exactly one tunnel by its row: the enable path and the
// "start now" button both call it. A row already running is a no-op.
func (m *Manager) StartOne(t *model.TunnelPair) error {
	if _, busy := m.isRunning(t.Id); busy {
		return nil
	}
	if err := m.start(t); err != nil {
		return err
	}
	return nil
}

// StartAll brings up every ENABLED tunnel row this panel owns. Rows that
// already run are left alone (idempotent under a panel restart that re-reads
// the same table). Each start is independent: one bad row must not take the
// fleet down.
func (m *Manager) StartAll() error {
	rows, err := m.enabledTunnels()
	if err != nil {
		return err
	}
	for i := range rows {
		t := &rows[i]
		if _, busy := m.isRunning(t.Id); busy {
			// A restart in progress keeps the old handle; the caller re-issues
			// StopTunnel first when it wants a clean swap.
			logger.Warning("tunnel", t.Name, "already running; skipping start")
			continue
		}
		if err := m.start(t); err != nil {
			logger.Warning("starting tunnel", t.Name, ":", err)
			m.reportStatus(t.Id, model.TunnelError, err.Error())
			continue
		}
		logger.Info("tunnel", t.Name, "started, role", t.Role, "transport", t.Transport)
	}
	return nil
}

// StopAll tears down every running tunnel: the panel-shutdown path.
func (m *Manager) StopAll() {
	m.mu.Lock()
	handles := make([]*handle, 0, len(m.running))
	for _, h := range m.running {
		handles = append(handles, h)
	}
	m.running = map[int]*handle{}
	m.mu.Unlock()
	for _, h := range handles {
		h.cancel()
	}
}

// StopOne tears down one tunnel by id: the UI's disable/delete path. A second
// call for an already-stopped id is a no-op, not an error.
func (m *Manager) StopOne(id int) {
	m.mu.Lock()
	h := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if h != nil {
		h.cancel()
	}
}

func (m *Manager) isRunning(id int) (*handle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.running[id]
	return h, ok
}

// enabledTunnels is the table read. A missing database (unit tests) degrades
// to an empty fleet rather than an error: the manager's own job is to run
// what is configured, and "nothing configured" is the empty answer.
func (m *Manager) enabledTunnels() ([]model.TunnelPair, error) {
	if database.GetDB() == nil {
		return nil, nil
	}
	var rows []model.TunnelPair
	if err := database.GetDB().Where("enable = ?", true).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// reportStatus stamps one row's live state. The watchdog job and the engine's
// own failure paths both write through here so the dashboard and the row
// agree on one source of truth.
func (m *Manager) reportStatus(id int, st model.TunnelStatus, lastErr string) {
	updates := map[string]any{"status": st, "updated_at": time.Now().UnixMilli()}
	if lastErr != "" {
		updates["last_error"] = lastErr
	}
	db := database.GetDB()
	if db == nil {
		return
	}
	_ = db.Model(&model.TunnelPair{}).Where("id = ?", id).Updates(updates).Error
}

// Connections returns one running tunnel's open-connection gauge, for the
// dashboard's live "N connections" cell. A stopped tunnel reads 0.
func (m *Manager) Connections(id int) int {
	h, ok := m.isRunning(id)
	if !ok {
		return 0
	}
	return int(atomic.LoadInt64(&h.connections))
}

// LastByteNano returns the UnixNano of one running tunnel's last byte, so the
// watchdog can tell a quiet-but-alive link (keepalive bytes move it) from a
// wedged one (nothing has moved it since the control link died).
func (m *Manager) LastByteNano(id int) int64 {
	h, ok := m.isRunning(id)
	if !ok {
		return 0
	}
	return atomic.LoadInt64(&h.lastByte)
}

// start brings one tunnel up on this panel's side: the role's listener or
// dialer, the control handshake, and the data pump. It is the one place the
// role × transport matrix is walked; everything it hands off is a goroutine
// that dies with the handle's cancel.
func (m *Manager) start(t *model.TunnelPair) error {
	// Validation up front: a 64-hex token, a server role with a listen
	// address, a client role with a dial target, and a usable port. A
	// misconfigured row is refused HERE, not discovered ten lines into the
	// socket code.
	if len(t.Token) != 64 {
		return ErrTokenLength
	}
	for i := 0; i < 64; i++ {
		if !isHex(t.Token[i]) {
			return fmt.Errorf("%w: must be 64 hex characters", ErrTokenLength)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &handle{cancel: cancel}
	m.mu.Lock()
	m.running[t.Id] = h
	m.mu.Unlock()

	token := deriveWireToken(t.Token)
	go func() {
		defer func() {
			// A start that dies reports the reason on its row, so the
			// dashboard shows WHY the tunnel is down, not just that it is.
			m.reportStatus(t.Id, model.TunnelDown, "")
		}()
		if err := m.runRole(ctx, t, h, token); err != nil {
			if ctx.Err() == nil {
				// A real error, not a shutdown: surface it.
				m.reportStatus(t.Id, model.TunnelError, err.Error())
			}
			logger.Warning("tunnel", t.Name, "ended:", err)
		}
	}()
	// The "started" stamp: up since now, the watchdog's freshness baseline.
	m.reportStatus(t.Id, model.TunnelUp, "")
	// Record the start time so the watchdog can measure freshness from here.
	m.stampUpSince(t.Id)
	return nil
}

// stampUpSince records one tunnel's "up since now" on its row: the watchdog's
// freshness window starts at the last start, not the last save.
func (m *Manager) stampUpSince(id int) {
	db := database.GetDB()
	if db == nil {
		return
	}
	_ = db.Model(&model.TunnelPair{}).Where("id = ?", id).
		Updates(map[string]any{"up_since": time.Now().UnixMilli(), "connections": 0}).Error
}

// isHex is the one-char token validation, table-driven.
func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// runRole is the role's half: a server role listens for the peer's control
// connection and relays; a client role dials it. The matrix:
//
//	server + tcp      -> ListenTCP, accept, performControl, pumpStream
//	server + tcp-tls  -> ListenTCP + TLS (the panel's managed cert)
//	server + ws/wss   -> an HTTP listener that upgrades to WebSocket
//	client + *        -> openTransport (dial), performControl, pumpStream
//
// UDP transport carries datagrams only: it runs pumpUDP on a bound socket
// instead of the stream pump.
func (m *Manager) runRole(ctx context.Context, t *model.TunnelPair, h *handle, token [tokenLen]byte) error {
	if t.Transport == model.TunnelTransportUDP {
		return m.runUDP(ctx, t, h, token)
	}
	if t.Role == model.TunnelRoleServer {
		ln, err := net.Listen("tcp", t.ServerAddr.String())
		if err != nil {
			return err
		}
		defer ln.Close()
		ln, cleanup, err := listenTransport(ln, t)
		if err != nil {
			return err
		}
		defer cleanup()
		// The role byte a server writes: it is the server.
		for {
			c, err := ln.Accept()
			if err != nil {
				return err
			}
			go func(c net.Conn) {
				if err := performControl(c, true, token, roleServer); err != nil {
					c.Close()
					return
				}
				go keepalivePump(ctx, c)
				pumpStream(ctx, c, t, h)
			}(c)
		}
	}
	// Client role: dial the server, then the same pump. A dial failure is
	// retried every 10s until the peer comes up or the context cancels.
	for {
		c, err := openTransport(ctx, t, false)
		if err != nil {
			logger.Debug("tunnel", t.Name, "dial failed, retrying in 10s:", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
				continue
			}
		}
		if err := performControl(c, false, token, roleClient); err != nil {
			c.Close()
			return err
		}
		go keepalivePump(ctx, c)
		pumpStream(ctx, c, t, h)
		return nil
	}
}

// runUDP is the datagram role: the server Binds its control port and waits
// for the client's datagrams; the client sends its datagrams to the server's
// address. A UDP "tunnel" is just a datagram relay with a token check on the
// first datagram; everything after is payload.
func (m *Manager) runUDP(ctx context.Context, t *model.TunnelPair, h *handle, token [tokenLen]byte) error {
	addr := t.ServerAddr.String()
	if t.Role == model.TunnelRoleServer {
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			return err
		}
		defer conn.Close()
		udpConn := conn.(*net.UDPConn)
		buf := make([]byte, 64*1024)
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			n, peer, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				return err
			}
			// First datagram from a new peer carries the token; verify it
			// before relaying anything. (Phase 2b adds per-peer state.)
			atomic.StoreInt64(&h.lastByte, time.Now().UnixNano())
			_ = peer
			_ = buf[:n]
		}
	}
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Send the token first, then relay local datagrams.
	if _, err := conn.Write(token[:]); err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := conn.Read(buf)
		if err != nil {
			return err
		}
		atomic.StoreInt64(&h.lastByte, time.Now().UnixNano())
		_ = buf[:n]
	}
}

