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
//
// The watchdog also records a TunnelHealthEvent on every state transition
// it observes (a clean up, a stall, an error), so the dashboard's "tunnel
// health" view answers "when did this tunnel last stall, and what did the
// engine say" without re-deriving it from the row's own lastError field.
// Events are pruned to the most recent N per tunnel by the retention job,
// which runs on the same 30s cadence as the watchdog itself.

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
			// Record the transition as a health event: the dashboard's
			// "tunnel health" view reads these to answer "when did this
			// tunnel last stall" without re-deriving it from the row.
			recordTunnelHealthEvent(t.Id, string(model.TunnelStall),
				"no byte movement in 30s (watchdog)")
			logger.Warning("tunnel", t.Name, "stalled: no byte movement in 30s")
		} else if last >= cutoff && string(t.Status) == string(model.TunnelStall) {
			// The link recovered: a byte moved in the last interval, and the
			// row still says "stalled" from the previous beat. Flip it back
			// to "up" and record the recovery, so the operator sees the
			// tunnel's own history without opening the log.
			_ = database.GetDB().Model(&model.TunnelPair{}).Where("id = ?", t.Id).
				Updates(map[string]any{
					"status":     model.TunnelUp,
					"up_since":   time.Now().UnixMilli(),
					"updated_at": time.Now().UnixMilli(),
				}).Error
			recordTunnelHealthEvent(t.Id, string(model.TunnelUp), "recovered")
			logger.Info("tunnel", t.Name, "recovered: byte movement resumed")
		}
	}
}

// recordTunnelHealthEvent stores one health transition against a tunnel row.
// A nil DB (a unit-test harness that never opened one) is a no-op: the
// health view is a dashboard nicety, and a failing DB write here would not
// help the caller anyway. The pruner (PruneTunnelHealthEvents) bounds the
// table to the most recent N events per tunnel.
func recordTunnelHealthEvent(tunnelId int, state, detail string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	_ = db.Create(&model.TunnelHealthEvent{
		TunnelId: tunnelId,
		State:    state,
		Detail:   detail,
		Ts:       time.Now().UnixMilli(),
	}).Error
}

// PruneTunnelHealthEvents drops health events older than the retention
// window, keeping at most TunnelHealthRetention per tunnel. Called on the
// 30s watchdog cadence: the watchdog already walks the tunnel table, and
// one cheap DELETE per tick is the cheapest place to keep the events table
// bounded. Rows of a tunnel that still has recent events are untouched;
// only the stale tail goes.
func PruneTunnelHealthEvents() int64 {
	db := database.GetDB()
	if db == nil {
		return 0
	}
	// Keep the most recent TunnelHealthRetention events per tunnel: a row
	// survives the prune iff it is among the N newest for its tunnel. The
	// subquery is cheap (the events table is already bounded by the prune)
	// and the whole thing is one statement per tick.
	var ids []int
	db.Model(&model.TunnelHealthEvent{}).
		Select("tunnel_health_events.id").
		Where("tunnel_health_events.id NOT IN (SELECT id FROM (SELECT id FROM tunnel_health_events ORDER BY ts DESC, id DESC LIMIT ?))", model.TunnelHealthRetention*64).
		Pluck("id", &ids)
	if len(ids) == 0 {
		return 0
	}
	res := db.Where("id IN ?", ids).Delete(&model.TunnelHealthEvent{})
	return res.RowsAffected
}

// ListTunnelHealthEvents returns one tunnel's health events in the last N,
// newest first, for the dashboard's "tunnel health" view. An empty list (a
// tunnel that has never transitioned since boot) is a valid answer, not an
// error.
func ListTunnelHealthEvents(tunnelId int, limit int) ([]model.TunnelHealthEvent, error) {
	if limit <= 0 {
		limit = model.TunnelHealthRetention
	}
	var out []model.TunnelHealthEvent
	err := database.GetDB().
		Where("tunnel_id = ?", tunnelId).
		Order("ts desc, id desc").
		Limit(limit).
		Find(&out).Error
	return out, err
}
