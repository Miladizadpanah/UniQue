package job

import (
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// CheckNodesJob polls the registered multi-node registry and refreshes each
// enabled remote panel's health snapshot. It is a pure reader of remote panels:
// it performs one bounded HTTP GET per node per tick and never writes to a
// remote, so a down or slow node only ever shows up as "offline", never takes
// the scheduler with it.
//
// A no-op when the registry is empty, so panels that never use multi-node pay
// nothing for it.
type CheckNodesJob struct{}

// NewCheckNodesJob creates the job.
func NewCheckNodesJob() *CheckNodesJob {
	return &CheckNodesJob{}
}

// Run polls every enabled node. A single poll's transport failure is already
// reflected in that node's online flag by the service; nothing to escalate.
func (j *CheckNodesJob) Run() {
	var nodeService service.NodeService
	if err := nodeService.PollAll(); err != nil {
		// PollAll only errors on a registry read failure; the per-node transport
		// errors are logged and stored per row, so this is the real problem.
		logger.Warning("node poll: ", err)
	}
}
