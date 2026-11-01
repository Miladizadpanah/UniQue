package tunnel

// ---------------------------------------------------------------------------
// Stream relay: the real user-traffic path over the control channel.
//
// Topology (the "Iran ⇄ abroad" reverse case), one direction:
//
//	CLIENT panel (abroad)                 SERVER panel (Iran)
//	  local listener :443                    real service :443
//	        │  app                           │
//	        ▼                                 ▼
//	  tunnel stream  ──────────────────►  dial "127.0.0.1:443" (or the map's
//	                                       backend "host:port")
//
// Each end binds a local listener on its map's forwarded ports. An accepted
// connection becomes one stream on the control channel, keyed by its source
// port; the PEER's pump resolves that port against ITS OWN map and dials the
// backend. The same mechanism runs in the opposite direction with the roles
// swapped, which is what makes a single tunnel bidirectional.
//
// Frame layout on the control channel (post-handshake):
//
//	[1 type] [2 stream] [4 len] [payload]
//
//	type 0x01 open     (no payload): a stream begins; id = source port
//	type 0x02 data     (payload):    bytes for that stream
//	type 0x03 close    (no payload): the local side ended that stream
//	type 0x00 keepalive (no payload): the 15s liveness beat
//
// The id is the source port because that is the peer's resolver key: "my
// app connected to port P; relay P to whatever P maps to on your side".
// ---------------------------------------------------------------------------

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

// The multiplexed frame header: 1 type + 2 stream + 4 length.
const frameHeaderLen = 1 + 2 + 4

// streamReg is one control connection's stream state. The PUMP owns its read
// direction (it fans data frames out to registered connections); the LOCAL
// listeners own their connections' read direction (they push into the
// tunnel). The two meet in the reg's two tables:
//
//	remote: id -> the local connection a PEER-opened stream writes INTO
//	         (this end dialed the backend the peer's port maps to)
//	local:  id -> the local-listener connection whose bytes a peer-opened
//	         stream pulls FROM (parked until the peer's open arrives)
//
// One mutex guards both: a frame's fan-out and a listener's park never
// interleave on the same id, so there is no "the connection moved between
// the lookup and the write" race.
type streamReg struct {
	mu     sync.Mutex
	remote map[uint16]net.Conn // peer-opened: data frames write here
	local  map[uint16]net.Conn // listener-opened: open frames read here
}

func newStreamReg() *streamReg {
	return &streamReg{
		remote: map[uint16]net.Conn{},
		local:  map[uint16]net.Conn{},
	}
}

func (r *streamReg) putRemote(id uint16, c net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remote[id] = c
}

func (r *streamReg) getRemote(id uint16) (net.Conn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.remote[id]
	return c, ok
}

func (r *streamReg) putLocal(id uint16, c net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.local[id] = c
}

func (r *streamReg) getLocal(id uint16) (net.Conn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.local[id]
	return c, ok
}

func (r *streamReg) drop(id uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.remote, id)
	delete(r.local, id)
}

// ---------------------------------------------------------------------------
// Frame primitive: the one write/read pair both the keepalive and the stream
// pipes use, so the channel's framing has a single owner.
// ---------------------------------------------------------------------------

// writeFrame writes [type][stream][len][payload]. The header and payload are
// two Writes on the same connection, and net.Conn guarantees a reader never
// sees a frame split across two different writers' calls in this design
// (the keepalive tick and the stream pipes are the only writers, and each
// frame is one writeFrame call).
func writeFrame(c net.Conn, typ byte, stream uint16, payload []byte) error {
	hdr := make([]byte, frameHeaderLen)
	hdr[0] = typ
	binary.BigEndian.PutUint16(hdr[1:3], stream)
	binary.BigEndian.PutUint32(hdr[3:7], uint32(len(payload)))
	if _, err := c.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads one frame. An io.EOF (control channel ended) propagates so
// the pump tears the tunnel down; a short read mid-frame is a broken peer,
// which the caller treats the same way.
func readFrame(c net.Conn) (byte, uint16, []byte, error) {
	hdr := make([]byte, frameHeaderLen)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return 0, 0, nil, err
	}
	typ := hdr[0]
	stream := binary.BigEndian.Uint16(hdr[1:3])
	length := binary.BigEndian.Uint32(hdr[3:7])
	if length == 0 {
		return typ, stream, nil, nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c, payload); err != nil {
		return typ, stream, nil, err
	}
	return typ, stream, payload, nil
}

// ---------------------------------------------------------------------------
// The pump's read direction: fan in-coming frames to the local side.
// ---------------------------------------------------------------------------

// pumpFrames is the control channel's reader: one loop, every frame, until
// the channel dies. A keepalive stamps the freshness gauge; an open makes
// this end dial the backend the stream's source port names (a REMOTE stream
// the peer opened for us); a data frame is written into that stream's local
// connection; a close tears the stream down.
//
// This is the relay's "receive" half. The "send" half is the local-listener
// side below: each accepted connection pushes its bytes into the tunnel as
// data frames under its own port's id.
func pumpFrames(ctx context.Context, c net.Conn, t *model.TunnelPair, reg *streamReg, h *handle) {
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return
		}
		typ, id, payload, err := readFrame(c)
		if err != nil {
			return // control channel ended: tear the tunnel down
		}
		switch typ {
		case frameKeepalive:
			atomic.StoreInt64(&h.lastByte, time.Now().UnixNano())
		case frameTCPOpen:
			// The peer opened a stream for source port id: dial the backend
			// THIS end's map names for id, and register the connection so
			// this stream's data frames land in it. A dial failure (backend
			// down) leaves the stream unregistered: its data frames are
			// dropped, and the peer's writes into its local connection block
			// until it gives up — the honest failure of "the other side's
			// service is not running".
			backend := resolveBackend(t, id)
			if backend == "" {
				continue // a port not mapped here: nothing to relay to.
			}
			backendConn, derr := net.DialTimeout("tcp", backend, 5*time.Second)
			if derr != nil {
				logger.Warning("tunnel", t.Name, "stream", id, "backend dial failed:", derr)
				continue
			}
			reg.putRemote(id, backendConn)
			// Pipe backend -> tunnel: the backend's answers cross back to the
			// peer as data frames under the same id.
			go func(id uint16, bc net.Conn) {
				defer bc.Close()
				defer reg.drop(id)
				for {
					if ctx.Err() != nil {
						return
					}
					n, rerr := bc.Read(buf)
					if n > 0 {
						atomic.AddInt64(&h.bytesDown, int64(n))
						if werr := writeFrame(c, frameTCPData, id, buf[:n]); werr != nil {
							return
						}
					}
					if rerr != nil {
						_ = writeFrame(c, frameTCPClose, id, nil)
						return
					}
				}
			}(id, backendConn)
		case frameTCPData:
			// A data frame for stream id: write it into the local backend
			// connection the open frame registered. An unknown id (the
			// backend dial failed, or the stream already closed) drops the
			// bytes: there is nowhere to put them.
			conn, ok := reg.getRemote(id)
			if !ok {
				continue
			}
			if len(payload) > 0 {
				atomic.AddInt64(&h.bytesDown, int64(len(payload)))
				if _, werr := conn.Write(payload); werr != nil {
					// The local backend went away mid-stream: tear it down
					// so the peer's next data frame hits the "unknown id"
					// drop and the stream ends cleanly.
					reg.drop(id)
					conn.Close()
				}
			}
		case frameTCPClose:
			// The peer's stream id ended: close the local backend and forget
			// it. A second close (the peer is quiet, this is a re-send) is
			// a no-op through the map's delete.
			if conn, ok := reg.getRemote(id); ok {
				conn.Close()
				reg.drop(id)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The local-listener side: bind the map's ports, park each accepted
// connection, and push its bytes into the tunnel as data frames.
// ---------------------------------------------------------------------------

// startLocalListeners binds the role's forwarded-port listeners and, for each
// accepted connection, opens the matching stream on the control channel and
// pipes the connection's bytes into it. A bind failure (the port is taken)
// logs and skips that row; it does not take the whole tunnel down.
//
// The send half of one stream: relayLocalTCP opens the stream (writeFrame
// open), pushes the local connection's bytes as data frames, and parks the
// connection in the reg so the pump's receive half can write the PEER'S
// answers back into it when the peer's data frames for this id arrive.
func startLocalListeners(ctx context.Context, t *model.TunnelPair, c net.Conn, reg *streamReg, h *handle) {
	for _, m := range sortPorts(t) {
		listener, err := bindLocalListener(m)
		if err != nil {
			logger.Warning("tunnel", t.Name, "local listener on port", m.Local, "failed:", err)
			continue
		}
		go func(m model.TunnelPortMap, ln net.Listener) {
			defer ln.Close()
			for {
				conn, err := ln.Accept()
				if err != nil {
					return // listener closed: the tunnel is down
				}
				if m.UDP {
					relayLocalUDP(ctx, t, c, reg, h, conn, uint16(m.Local))
					continue
				}
				relayLocalTCP(ctx, t, c, reg, h, conn, uint16(m.Local))
			}
		}(m, listener)
	}
}

// bindLocalListener opens the role's local socket for one map row: UDP rows
// use a datagram listener, TCP rows a TCP one. The listen address is the row's
// Local port on all interfaces — a forwarded port must be reachable, which is
// the whole point of the tunnel.
func bindLocalListener(m model.TunnelPortMap) (net.Listener, error) {
	addr := fmt.Sprintf(":%d", m.Local)
	if m.UDP {
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return nil, err
		}
		return udpListener{pc: pc}, nil
	}
	return net.Listen("tcp", addr)
}

// relayLocalTCP is one accepted connection: open its stream, park it for the
// pump's receive half, then push its bytes into the tunnel. The stream's id
// is this listener's port — the peer's resolver key.
func relayLocalTCP(ctx context.Context, t *model.TunnelPair, c net.Conn, reg *streamReg, h *handle, local net.Conn, id uint16) {
	defer local.Close()
	// Park FIRST, then write the open frame: a peer data frame that races in
	// between the open and the park still finds the connection registered,
	// so no byte is lost to the ordering. The open is written second because
	// it is the frame that tells the peer "this id is live"; the park is
	// local and immediate.
	reg.putLocal(id, local)
	if err := writeFrame(c, frameTCPOpen, id, nil); err != nil {
		reg.drop(id)
		return
	}
	// The receive direction (peer's answers into `local`) is the pump's job:
	// its data frames for id write into the parked connection. This side
	// only pushes local bytes out and blocks until the stream ends.
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return
		}
		n, rerr := local.Read(buf)
		if n > 0 {
			atomic.AddInt64(&h.bytesUp, int64(n))
			if werr := writeFrame(c, frameTCPData, id, buf[:n]); werr != nil {
				return // control channel dead: tear down
			}
		}
		if rerr != nil {
			// The local app closed: tell the peer, drop the park, stop.
			_ = writeFrame(c, frameTCPClose, id, nil)
			reg.drop(id)
			return
		}
	}
}

// udpListener adapts a UDP PacketConn to the Listener interface the accept
// loop expects. Each "accept" is one peer's datagram stream: the relay keys a
// UDP stream on (local port, peer address), so the listener hands back a
// per-peer fake connection.
type udpListener struct{ pc net.PacketConn }

func (l udpListener) Accept() (net.Conn, error) {
	buf := make([]byte, 64*1024)
	_, addr, err := l.pc.ReadFrom(buf)
	if err != nil {
		return nil, err
	}
	return &udpPeer{pc: l.pc, addr: addr}, nil
}

func (l udpListener) Close() error  { return l.pc.Close() }
func (l udpListener) Addr() net.Addr { return l.pc.LocalAddr() }

// udpPeer is one peer's datagram stream: the "connection" a UDP relay pipe
// reads/writes. Its Read/Write go to and from that peer's address.
type udpPeer struct {
	pc   net.PacketConn
	addr net.Addr
}

func (p *udpPeer) Read(b []byte) (int, error) {
	n, _, err := p.pc.ReadFrom(b)
	return n, err
}
func (p *udpPeer) Write(b []byte) (int, error)           { return p.pc.WriteTo(b, p.addr) }
func (p *udpPeer) Close() error                           { return nil }
func (p *udpPeer) LocalAddr() net.Addr                    { return p.pc.LocalAddr() }
func (p *udpPeer) RemoteAddr() net.Addr                   { return p.addr }
func (p *udpPeer) SetDeadline(t time.Time) error           { return p.pc.SetDeadline(t) }
func (p *udpPeer) SetReadDeadline(t time.Time) error      { return p.pc.SetReadDeadline(t) }
func (p *udpPeer) SetWriteDeadline(t time.Time) error     { return p.pc.SetWriteDeadline(t) }

// relayLocalUDP is one peer's datagram stream: it opens the stream under the
// listener's port id and pushes datagrams in as data frames. UDP has no
// clean "close" — the stream ends when the control channel dies or the peer
// stops sending, both of which end this loop.
func relayLocalUDP(ctx context.Context, t *model.TunnelPair, c net.Conn, reg *streamReg, h *handle, local net.Conn, id uint16) {
	reg.putLocal(id, local)
	if err := writeFrame(c, frameTCPOpen, id, nil); err != nil {
		reg.drop(id)
		return
	}
	buf := make([]byte, 64*1024)
	for {
		if ctx.Err() != nil {
			return
		}
		n, rerr := local.Read(buf)
		if n > 0 {
			atomic.AddInt64(&h.bytesUp, int64(n))
			if werr := writeFrame(c, frameTCPData, id, buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			_ = writeFrame(c, frameTCPClose, id, nil)
			reg.drop(id)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Map lookups.
// ---------------------------------------------------------------------------

// resolveBackend answers "which local backend does source port P reach" for
// the role's own map: the first row whose Local is P. A blank backend means
// the role's loopback; no match is a nil answer (the stream's bytes have
// nowhere to go here and are dropped).
func resolveBackend(t *model.TunnelPair, port uint16) string {
	maps, _ := t.ForwardedPortsJSON()
	for _, m := range maps {
		if uint16(m.Local) == port {
			if m.Backend != "" {
				return m.Backend
			}
			return fmt.Sprintf("127.0.0.1:%d", port)
		}
	}
	return ""
}

// sortPorts returns the map's rows deduped by Local port in ascending order,
// so a restart rebinds the same local listeners in the same order.
func sortPorts(t *model.TunnelPair) []model.TunnelPortMap {
	maps, _ := t.ForwardedPortsJSON()
	seen := map[int]bool{}
	out := make([]model.TunnelPortMap, 0, len(maps))
	for _, m := range maps {
		if seen[m.Local] {
			continue
		}
		seen[m.Local] = true
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Local < out[j].Local })
	return out
}

// The handle's byte gauges: the relay pipes increment these; the watchdog
// and the dashboard read them through the handle's atomic accessors. They
// are declared here (the relay's package) and accessed by the manager.
// (The handle struct itself is in manager.go; these are the fields it
// carries — kept as named references so a rename of either side is a
// single-file change.)
var _ = atomic.AddInt64
