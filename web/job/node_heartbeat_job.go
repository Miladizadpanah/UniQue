package job

// NodeHeartbeatJob is the NODE side of the push telemetry: every 30s it
// re-renders this panel's NodeStatus (system gauges + counts + version) and,
// when the operator has told this panel which hub it reports to, POSTs it to
// the hub's /panel/node/heartbeat endpoint so the hub's row updates on the
// push cadence without waiting for the hub's own 60s poll.
//
// No-config default: the panel just re-renders locally. The hub still polls;
// a push target is what upgrades the hub's view from "poll snapshot" to
// "live gauges on a 30s rhythm".

import (
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// NewNodeHeartbeatJob creates the job. The interval is set at registration
// time in web/web.go (every 30s); this type carries no state.
func NewNodeHeartbeatJob() *NodeHeartbeatJob {
	return &NodeHeartbeatJob{}
}

type NodeHeartbeatJob struct{}

// Run is one beat. The hub target is read from the panel's settings each beat
// (an operator can point the panel at a new hub without a restart): blank base
// = push disabled.
func (j *NodeHeartbeatJob) Run() {
	var s service.SettingService
	hubBase, _ := s.GetNodeHubUrl()
	hubToken, _ := s.GetNodeHubToken()
	if hubBase == "" {
		// No push target configured: still re-render locally so the "this
		// panel" row on the dashboard (and any future local consumer of the
		// gauges) sees a fresh reading.
		_ = service.BuildHeartbeatPayload()
		return
	}
	service.RunNodeHeartbeatJobTick(hubBase, hubToken)
}
