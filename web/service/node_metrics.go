package service

// Node metrics history: one sampled row per gauge beat, so the dashboard can
// chart a node's CPU/RAM/network history instead of showing only its last
// reading. The table is bounded by the retention window (drop rows older than
// NodeMetricsRetention); a 30s beat kept for 24h is 288 rows per node, which
// is trivial next to the inbounds table it summarizes.
//
// Sampled gauges:
//
//	CpuPct  panel's own 1s-sampled CPU figure (fillHeartbeatMetrics).
//	MemPct  used over total host RAM.
//	NetUp   cumulative bytes SENT on the host since boot.
//	NetDown cumulative bytes RECEIVED on the host since boot.
//
// NetUp/NetDown are counters, not rates: the UI's chart computes the delta
// between consecutive samples (first sample is a flat baseline, the second
// shows the rate). This keeps a node restart (which resets the kernel
// counters) from reading as a giant negative spike.
//
// Sources:
//
//	"heartbeat"  a PUSH beat the hub recorded (RecordHeartbeat).
//	"poll"       a PULL poll the hub made (Poll, on success).
//	"local"      this panel's own local heartbeat (no hub, or a hub that
//	             never polls this node yet): the panel keeps its own
//	             history so the "this panel" row on the dashboard has a
//	             chart even when no other panel is watching it.

import (
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// NodeMetricsRetention is how long a gauge sample is kept. 24h covers a full
// day of 30s beats (288 rows/node); the retention job prunes the rest. A
// longer window costs nothing on SQLite until the operator actually pushes
// thousands of nodes, which is not the case yet.
const NodeMetricsRetention = 24 * time.Hour

// NodeMetricsRetentionHours is the retention in hours, for the UI's "show the
// last N hours" picker. Kept as a separate constant so a change to the
// retention duration cannot silently desync the picker from the pruner.
const NodeMetricsRetentionHours = 24

// RecordNodeMetrics stores one gauge sample against a node's row. A nil DB
// (a unit-test harness that never opened one) is a no-op, not an error: the
// chart is a nice-to-have, and a failing DB write here would not help the
// caller anyway.
func RecordNodeMetrics(nodeId int, source string, st *model.NodeStatus) {
	if st == nil {
		return
	}
	db := database.GetDB()
	if db == nil {
		return
	}
	_ = db.Create(&model.NodeMetrics{
		NodeId:  nodeId,
		Source:  source,
		CpuPct:  st.CpuPct,
		MemPct:  st.MemPct,
		NetUp:   st.NetUp,
		NetDown: st.NetDown,
		Ts:      time.Now().UnixMilli(),
	}).Error
}

// RecordLocalMetrics is this panel's own gauge beat: it re-renders its Local
// status and stores the sample against ITS OWN node row (when one exists; a
// panel that no hub tracks yet has no row, and the sample is dropped, which
// is the same no-op as RecordNodeMetrics on a nil DB). This is what gives the
// "this panel" row on the dashboard a history chart even on a single-node
// install.
func RecordLocalMetrics() {
	st := (&NodeService{}).LocalStatus()
	db := database.GetDB()
	if db == nil || st == nil {
		return
	}
	// Find this panel's own node row by name (the operator's label); if it
	// does not exist yet (no hub registered it), there is nothing to bind to.
	var row model.Node
	if err := db.Where("name = ?", localNodeRowName()).First(&row).Error; err != nil {
		return // no row: single-node install, nothing to chart.
	}
	_ = db.Create(&model.NodeMetrics{
		NodeId:  row.Id,
		Source:  "local",
		CpuPct:  st.CpuPct,
		MemPct:  st.MemPct,
		NetUp:   st.NetUp,
		NetDown: st.NetDown,
		Ts:      time.Now().UnixMilli(),
	}).Error
}

// localNodeRowName is the label the operator gave this panel: the mTLS hub
// CN, which is also the name the hub registers this node under. A blank name
// means "the operator has not named this panel" and the local sample has no
// row to bind to — the caller treats it as a no-op.
func localNodeRowName() string {
	var s SettingService
	name, _ := s.GetServerName()
	return name
}

// ListNodeMetricsHistory returns one node's gauge samples in the last `hours`
// hours, oldest first, for the chart. hours <= 0 asks for the retention
// window. An empty list (a node that has not pushed since the window opened)
// is a valid answer, not an error.
func ListNodeMetricsHistory(nodeId int, hours int) ([]model.NodeMetrics, error) {
	if hours <= 0 {
		hours = NodeMetricsRetentionHours
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
	var out []model.NodeMetrics
	err := database.GetDB().
		Where("node_id = ? AND ts >= ?", nodeId, cutoff).
		Order("ts asc").
		Find(&out).Error
	return out, err
}

// PruneNodeMetrics drops gauge samples older than the retention window, for
// every node. It returns the number of rows dropped, which the caller can
// report ("pruned 1,204 samples") or ignore. Called on the 60s node poll
// cadence: the poll already walks the table, and one cheap DELETE per tick is
// the cheapest place to keep the table bounded. Rows of a node that still has
// beats inside the window are untouched; only the stale tail goes.
func PruneNodeMetrics() int64 {
	db := database.GetDB()
	if db == nil {
		return 0
	}
	cutoff := time.Now().Add(-NodeMetricsRetention).UnixMilli()
	res := db.Where("ts < ?", cutoff).Delete(&model.NodeMetrics{})
	return res.RowsAffected
}
