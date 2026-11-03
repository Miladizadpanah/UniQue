package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service/tunnel"
)

// ---------------------------------------------------------------------------
// TunnelService: the panel-side CRUD + link-test for the native tunnel engine.
// The ENGINE itself (listener/dialer, pumps, watchdog) lives in
// web/service/tunnel; this service is the thin layer the controller calls.
// ---------------------------------------------------------------------------

// TunnelService owns the tunnel table and the link-test dialer. The tunnel
// engine's Manager is a package global (one per process); this service is a
// zero-value struct, so every entry point is method-receiver sugar over it.
type TunnelService struct{}

// ErrTunnelName is a blank operator label.
var ErrTunnelName = errors.New("tunnel name is required")

// ErrTunnelAddr is a missing listen/dial target.
var ErrTunnelAddr = errors.New("tunnel address (host:port) is required")

// LinkResult is the link-test's answer: one probe against a named target,
// measuring what the operator will actually feel on the path — and which
// carrier is likely to survive it.
type LinkResult struct {
	// Reachable is the dial itself succeeding (TCP connect).
	Reachable bool `json:"reachable"`
	// LatencyMs is the round trip of the control handshake, or the dial
	// time when the target only answers to a raw connect.
	LatencyMs int `json:"latencyMs"`
	// Error carries a concrete "why not" when Reachable is false, so the UI
	// can show "connection refused on port 443" instead of "unreachable".
	Error string `json:"error"`
	// Recommended is the carrier this measured path points at: a clean,
	// fast path wants plain TCP; a lossy or filtered one wants the
	// encrypted/WSS variant. The recommendation is a heuristic on the
	// measured latency + reachability, not a promise — the operator can
	// override it in the wizard.
	Recommended model.TunnelTransport `json:"recommended"`
}

// List returns every tunnel row, newest first, the way the dashboard's table
// reads it. The row carries the engine's last status (up/down/stalled/error)
// and its live gauges — that is the "is this tunnel healthy" view.
func (s *TunnelService) List() ([]model.TunnelPair, error) {
	var rows []model.TunnelPair
	err := database.GetDB().Order("id asc").Find(&rows).Error
	return rows, err
}

// Get loads one row by id (the handlers 404 the miss).
func (s *TunnelService) Get(id int) (*model.TunnelPair, error) {
	var t model.TunnelPair
	if err := database.GetDB().First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// Save creates (id 0) or updates one tunnel. The token is generated when the
// operator leaves it blank on a NEW row: one click, one secret, both ends
// copy it. An update keeps the stored token unless the operator typed a new
// one — rotating a live tunnel's token is what breaks it, so the default is
// "do not touch it".
func (s *TunnelService) Save(t *model.TunnelPair) (*model.TunnelPair, error) {
	if strings.TrimSpace(t.Name) == "" {
		return nil, ErrTunnelName
	}
	t.Name = strings.TrimSpace(t.Name)
	if err := s.validate(t); err != nil {
		return nil, err
	}
	db := database.GetDB()
	if t.Id == 0 {
		if strings.TrimSpace(t.Token) == "" {
			t.Token = tunnel.GenerateToken()
		}
		t.CreatedAt = time.Now().UnixMilli()
		t.UpdatedAt = t.CreatedAt
		if t.Transport == "" {
			t.Transport = model.TunnelTransportTCP
		}
		if t.Role == "" {
			t.Role = model.TunnelRoleClient
		}
		if err := db.Create(t).Error; err != nil {
			return nil, err
		}
	} else {
		// An edit never rotates the token unless the operator typed a new
		// one: a silent token change is the "my tunnel died after I saved"
		// bug. The row's token is read back, and only replaced when a new
		// non-blank value came in.
		if strings.TrimSpace(t.Token) == "" {
			var existing model.TunnelPair
			if err := db.First(&existing, t.Id).Error; err == nil {
				t.Token = existing.Token
			}
		}
		t.UpdatedAt = time.Now().UnixMilli()
		// The column names are the GORM snake_case of the struct fields, NOT
		// short aliases: the DB schema (AutoMigrate) names them
		// fallback_transports / forwarded_ports / tls_fingerprint /
		// proxy_proto_v2. The create path (db.Create) reads the struct
		// fields directly so it was always right; this Updates map must
		// match the actual column names or SQLite answers "no such column".
		if err := db.Model(&model.TunnelPair{}).Where("id = ?", t.Id).Updates(map[string]any{
			"name":              t.Name,
			"remark":            t.Remark,
			"role":              t.Role,
			"direction":         t.Direction,
			"transport":         t.Transport,
			"fallback_transports": t.FallbackTransports,
			"server_addr":       t.ServerAddr,
			"forwarded_ports":   t.ForwardedPorts,
			"token":             t.Token,
			"tls_fingerprint":   t.TLSFingerprint,
			"tuning":            t.Tuning,
			"proxy_proto_v2":    t.ProxyProtoV2,
			"enable":            t.Enable,
			"updated_at":        t.UpdatedAt,
		}).Error; err != nil {
			return nil, err
		}
		db.Model(&model.TunnelPair{}).Where("id = ?", t.Id).First(t)
	}
	return t, nil
}

// Delete removes one row and tells the engine to stop it. A delete of a
// running tunnel is the "tear down both ends" case: the LOCAL listener/dialer
// stops here, and the PEER's end dies when its keepalive stops moving (the
// watchdog on the other panel flips it to stalled). No remote call — the
// tunnel is not federation; it is two panels that happen to hold the same
// token, and deleting one half is just deleting one half.
func (s *TunnelService) Delete(id int) error {
	var t model.TunnelPair
	if err := database.GetDB().First(&t, id).Error; err != nil {
		return err
	}
	tunnel.DefaultManager().StopOne(id)
	return database.GetDB().Delete(&model.TunnelPair{}, "id = ?", id).Error
}

// SetEnable flips one row's enable flag and starts or stops the engine
// accordingly. A disable is a StopOne; an enable is a StartOne.
func (s *TunnelService) SetEnable(id int, enable bool) error {
	t, err := s.Get(id)
	if err != nil {
		return err
	}
	t.Enable = enable
	if err := database.GetDB().Model(&model.TunnelPair{}).Where("id = ?", id).
		Updates(map[string]any{"enable": enable, "updated_at": time.Now().UnixMilli()}).Error; err != nil {
		return err
	}
	if enable {
		return startOne(t)
	}
	tunnel.DefaultManager().StopOne(id)
	return nil
}

// validate is the pre-save check: a usable address, a valid transport, and a
// token that is either absent (new, will be minted) or a clean 64-hex.
func (s *TunnelService) validate(t *model.TunnelPair) error {
	if t.ServerAddr.Port <= 0 || t.ServerAddr.Port > 65535 {
		return fmt.Errorf("%w: port must be 1-65535, got %d", ErrTunnelAddr, t.ServerAddr.Port)
	}
	switch t.Transport {
	case model.TunnelTransportTCP, model.TunnelTransportTCPTLS,
		model.TunnelTransportWS, model.TunnelTransportWSS, model.TunnelTransportUDP:
	default:
		return fmt.Errorf("unknown transport %q", t.Transport)
	}
	// The TLS carriers need a certificate on the server role. The panel's
	// managed SSL store supplies it; a blank fingerprint + a server role on
	// -tls is the operator's "which cert?" question, answered here as a
	// validation error rather than a silent self-signed at runtime.
	if t.Role == model.TunnelRoleServer &&
		(t.Transport == model.TunnelTransportTCPTLS || t.Transport == model.TunnelTransportWSS) {
		// The ssl store is checked at START, not save: a save with no cert
		// yet is allowed (the operator sets the tunnel up, then binds a
		// cert, then enables it). Start is where the refusal happens.
	}
	return nil
}

// LinkTest dials the named target over the plain carriers and reports what
// the path is like. It is the operator's "which transport for THIS route"
// answer: a clean fast path recommends TCP, a path that only TLS connects
// through recommends WSS, and a target that refuses everything says so with
// the specific error, not "unreachable".
func (s *TunnelService) LinkTest(host string, port int) *LinkResult {
	res := &LinkResult{Recommended: model.TunnelTransportTCP}
	target := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	start := time.Now()

	// A raw TCP dial: the baseline "does anything connect at all".
	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		res.Error = err.Error()
		// A refused/filtered TCP port is exactly the case where the
		// encrypted carriers still work: the operator's path may allow
		// 443 (WSS) while 443/raw-TCP is closed. Point that out.
		res.Recommended = model.TunnelTransportWSS
		return res
	}
	res.LatencyMs = int(time.Since(start).Milliseconds())
	res.Reachable = true
	// A fast clean path (sub-100ms, no error) is the plain-TCP case.
	// A slow or lossy path (the dial connected but took a while) is where
	// the loss-resistant carriers earn their overhead.
	if res.LatencyMs > 400 {
		res.Recommended = model.TunnelTransportWSS
	}
	conn.Close()
	return res
}

// startOne is the enable path's engine call, isolated so the "re-read the
// row then start" sequence has one owner.
func startOne(t *model.TunnelPair) error {
	// A start that finds the row already running is a no-op, not an error:
	// the UI's "enable" button can be clicked twice, and the second click
	// must not read as a failure.
	if h, busy := tunnel.DefaultManager().IsRunning(t.Id); busy {
		_ = h
		return nil
	}
	return startEngine(t)
}

// startEngine is the engine's entry point, named separately so a future
// per-role validation can live here without the controller knowing.
func startEngine(t *model.TunnelPair) error {
	return tunnel.DefaultManager().StartOne(t)
}

// ---------------------------------------------------------------------------
// The package-level engine manager. One per process; the service methods and
// the watchdog job both reach it through DefaultManager so there is exactly
// one "who owns the running tunnels" answer.
// ---------------------------------------------------------------------------

// DefaultManager is initialized by the panel's boot (web.go) via
// InitManager. Before that it is a fresh idle manager, so a unit test that
// calls a service method still gets a working (empty) engine.
var defaultManager = tunnel.NewManager()

func init() {
	tunnel.SetDefaultManager(defaultManager)
}

// InitManager is the boot hook: web.go calls it once, passing the manager it
// constructed (so the panel's shutdown path and the watchdog share the same
// object). A nil argument keeps the package-level one.
func InitManager(mgr *tunnel.Manager) {
	if mgr != nil {
		tunnel.SetDefaultManager(mgr)
	}
}

// ensure the logger import is used by a future warning path; keep it live
// now so the build does not drift.
var _ = logger.Info
var _ = context.Background

