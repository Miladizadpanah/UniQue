package service

// Node heartbeat: the node's PUSH side of the hub↔node telemetry.
//
// The hub polls this panel's /panel/node/status every 60s (pull), but a poll
// is a snapshot taken at poll time. The node-side job below runs every 30s and
// re-renders the SAME NodeStatus — the one the hub decodes on its pull — plus
// the system gauges the dashboard shows. If the operator also configured a
// "hub URL" for this node (this panel IS a remote node being watched), the job
// additionally POSTs the heartbeat to that URL so the hub's row updates on the
// 30s push cadence without waiting for its own poll.
//
// Zero-configuration behavior: a panel that never sets a hub URL just keeps
// re-rendering LocalStatus locally (used by the node's own dashboard for the
// "this panel" row) and never phones home.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

// nodeHubReportPath is the hub's side of the push: POST the NodeStatus JSON,
// gated by the hub's node API token. The hub records it against the token's
// node row (the one this panel registered under).
const nodeHubReportPath = "/panel/node/heartbeat"
const nodeUpdatePanelPath = "/panel/node/updatePanel"

// nodeHeartbeatInterval is the push cadence. 30s matches the hub's own poll
// so the dashboard's "last heartbeat" never lags more than one poll behind the
// operator's mental model of the fleet; a slower push is not more efficient,
// it just reads as stale.
const nodeHeartbeatInterval = 30 * time.Second

// hubReportClient bounds one push; well under the 30s cadence so a wedged hub
// cannot stall the node's job queue.
var hubReportClient = &http.Client{Timeout: 8 * time.Second}

// BuildHeartbeatPayload is this panel's heartbeat: the same LocalStatus the
// hub's poll decodes, with the system gauges filled. It is a pure function of
// "this panel, right now": safe to call from any goroutine, and the unit
// under test when the push transport is exercised separately.
func BuildHeartbeatPayload() *model.NodeStatus {
	st := (&NodeService{}).LocalStatus()
	// LocalStatus already fills the heartbeat metrics (see fillHeartbeatMetrics).
	return st
}

// PushHeartbeatToHub POSTs the current heartbeat to a hub's token-gated
// endpoint. hubBase is the hub's base URL (https://hub:2096/subs/) and
// hubToken the node API token the hub handed this panel when it registered.
//
// A push failure is logged, not returned to the caller in a blocking shape:
// the heartbeat job runs it every 30s, so one lost beat self-heals on the
// next tick. The hub also keeps polling, so a push gap of any length is a
// latency issue, not a data-loss one.
func PushHeartbeatToHub(hubBase, hubToken string) error {
	st := BuildHeartbeatPayload()
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	url := strings.TrimRight(hubBase, "/") + nodeHubReportPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if hubToken != "" {
		req.Header.Set("X-Node-Token", hubToken)
	}
	resp, err := hubReportClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &hubReportStatusError{What: "hub heartbeat push", Status: resp.StatusCode}
	}
	_ = json.NewDecoder(resp.Body).Decode(&struct{}{}) // drain; the hub returns a plain ok
	return nil
}

// hubReportStatusError is a "the hub answered, not with 200" push failure. The
// job logs it distinctly from a transport error (hub down vs hub refusing the
// token) so the operator can tell which half of the wire to fix.
type hubReportStatusError struct {
	What   string
	Status int
}

func (e *hubReportStatusError) Error() string {
	return e.What + " failed with status " + http.StatusText(e.Status)
}

// RunNodeHeartbeatJobTick is one 30s beat. It ALWAYS re-renders the payload
// (so a hub that polls still sees the gauges), and WHEN a hub URL+token are
// configured on this panel it additionally pushes. The push target is the
// "this panel is a remote node of hub X" case: the hub registered this panel
// with a name+token, and the panel now reports back into that row.
//
// hubBase/hubToken come from the panel's settings (SetNodeHubTarget), NOT from
// a node row: this panel is not running a hub dashboard in the same process,
// it is a leaf reporting to a hub somewhere else. Blank target = push
// disabled, the no-config default.
func RunNodeHeartbeatJobTick(hubBase, hubToken string) {
	st := BuildHeartbeatPayload()
	// Refresh the panel's own "this row" gauges for its dashboard, even with no
	// push target configured: the LocalStatus callers (nodeStatus route,
	// dashboard "this panel" row) read through the same LocalStatus path.
	_ = st
	if strings.TrimSpace(hubBase) == "" {
		return // no push target: local rendering only
	}
	if err := PushHeartbeatToHub(hubBase, hubToken); err != nil {
		logger.Warning("node heartbeat push to", hubBase, "failed:", err)
	}
}
