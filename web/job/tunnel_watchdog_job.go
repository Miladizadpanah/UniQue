package job

// TunnelWatchdogJob is the native tunnel engine's self-healer: every 30s it
// walks the running tunnels and reports one that has stopped moving bytes
// (a wedged control link) back to "stalled" on its row, so the dashboard
// and the operator see the real state instead of a row that says "up" while
// nothing is actually flowing.
//
// A quiet-but-ALIVE tunnel does not trip it: the engine's keepalive pump
// moves a byte every 15s on the control channel, so a tunnel carrying no
// user traffic still has a moving lastByte stamp. Only a genuinely stuck
// link — the dial succeeded but the peer stopped answering — goes still, and
// that is exactly the "stalled" the operator means by "my tunnel died but
// the panel says it's fine".

import (
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service/tunnel"
)

// NewTunnelWatchdogJob creates the job. The interval is set at registration
// time in web/web.go (every 30s); this type carries no state.
func NewTunnelWatchdogJob() *TunnelWatchdogJob {
	return &TunnelWatchdogJob{}
}

type TunnelWatchdogJob struct{}

// Run is one beat: walk the running tunnels, and for each one whose last
// byte is older than two keepalive intervals (30s of silence on a link that
// pings every 15s), mark it stalled on its row. A tunnel that has no
// running handle (never started, or stopped) is left alone — its row
// already says down.
func (j *TunnelWatchdogJob) Run() {
	if database.GetDB() == nil {
		return // no table yet (unit-test harness): nothing to watch.
	}
	mgr := tunnel.DefaultManager()
	cutoff := time.Now().Add(-30 * time.Second).UnixNano()

	var rows []model.TunnelPair
	if err := database.GetDB().Where("enable = ?", true).Find(&rows).Error; err != nil {
		logger.Warning("tunnel watchdog: reading rows:", err)
		return
	}
	for i := range rows {
		t := &rows[i]
		if _, running := mgr.IsRunning(t.Id); !running {
			continue // no handle: the row's own status already says down.
		}
		last := mgr.LastByteNano(t.Id)
		if last == 0 {
			continue // just started, keepalive will stamp within 15s.
		}
		if last < cutoff && string(t.Status) != string(model.TunnelStall) {
			// Stalled: a transient blip should not kill a tunnel that will
			// reconnect on the next dial; the operator's "disable" button is
			// the explicit teardown. The row is marked so the dashboard
			// shows it; the engine's context keeps running.
			_ = database.GetDB().Model(&model.TunnelPair{}).Where("id = ?", t.Id).
				Updates(map[string]any{
					"status":     model.TunnelStall,
					"updated_at": time.Now().UnixMilli(),
				}).Error
			logger.Warning("tunnel", t.Name, "stalled: no byte movement in 30s")
		}
	}
}
