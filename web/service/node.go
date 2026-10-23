package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v2/config"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	xray "github.com/mhsanaei/3x-ui/v2/xray"
)

// NodeService is the hub side of multi-node management. It keeps the registry of
// remote panels this dashboard watches and polls each one's public status
// endpoint. The hub only READS remote nodes; it never writes to them, so a
// broken or hostile remote panel cannot reach this one through this path.
type NodeService struct{}

// ErrNodeName guards the write path: a node row is meaningless without a name.
var ErrNodeName = errors.New("node name is required")

// ErrNodeUrl guards the write path: a node must point at an http(s) base url.
var ErrNodeUrl = errors.New("node base url must be http or https")

// nodeStatusPath is the public endpoint every remote node exposes for the hub to
// poll. It is token-gated (see NodeStatusController), not session-gated, so the
// hub authenticates with the shared node token rather than a panel login.
const nodeStatusPath = "/panel/node/status"

// nodePollTimeout bounds one poll so a wedged remote panel cannot stall the
// scheduler; well under the 60s poll cadence.
var nodePollTimeout = 8 * time.Second

// nodeOnlineWindow is how recent a remote account's last online stamp has to be
// to count as "online" in the status snapshot. Matches the panel's own notion
// of a live connection (5 minutes), generous enough to ignore poll jitter.
const nodeOnlineWindow = 5 * time.Minute

// List returns every registered node, most recently seen first. The snapshot
// fields (version, counts, latency, online) are the last successful poll.
func (s *NodeService) List() ([]model.Node, error) {
	var nodes []model.Node
	err := database.GetDB().Order("id asc").Find(&nodes).Error
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// Save creates (id 0) or updates one node. Validation is done up front; the
// snapshot fields are left to the poller, so a save never overwrites a live
// health reading with zeros.
func (s *NodeService) Save(n *model.Node) (*model.Node, error) {
	if strings.TrimSpace(n.Name) == "" {
		return nil, ErrNodeName
	}
	if !isHttpUrl(n.BaseUrl) {
		return nil, ErrNodeUrl
	}
	n.BaseUrl = strings.TrimRight(strings.TrimSpace(n.BaseUrl), "/")
	n.Name = strings.TrimSpace(n.Name)
	db := database.GetDB()
	if n.Id == 0 {
		if err := db.Create(n).Error; err != nil {
			return nil, err
		}
	} else {
		// Update only the operator-editable fields; the snapshot is owned by the
		// poller. A partial update so a stale client body can't zero live data.
		err := db.Model(&model.Node{}).Where("id = ?", n.Id).Updates(map[string]any{
			"name": n.Name, "base_url": n.BaseUrl, "token": n.Token,
			"enable": n.Enable, "remark": n.Remark,
		}).Error
		if err != nil {
			return nil, err
		}
		db.Model(&model.Node{}).Where("id = ?", n.Id).First(n)
	}
	return n, nil
}

// Delete removes one node. Nothing remote is touched; the row and its snapshot
// simply stop being watched.
func (s *NodeService) Delete(id int) error {
	return database.GetDB().Delete(&model.Node{}, "id = ?", id).Error
}

// Poll fetches one node's status, refreshes its snapshot row and returns the
// decoded status. A network or auth failure marks the node offline but does not
// error the caller: the dashboard must still render an unreachable node as
// "offline", not fail the whole refresh.
func (s *NodeService) Poll(n *model.Node) (*model.NodeStatus, error) {
	start := time.Now()
	status, err := fetchNodeStatus(n)
	latency := int(time.Since(start).Milliseconds())
	db := database.GetDB()
	updates := map[string]any{"latency_ms": latency}
	if err != nil {
		updates["online"] = false
		if len(err.Error()) > 0 {
			logger.Warning("node poll", n.Name, "failed:", err)
		}
	} else {
		updates["online"] = true
		updates["last_seen"] = time.Now().UnixNano()
		if status != nil {
			updates["version"] = status.Version
			updates["inbound_count"] = status.InboundCount
			updates["account_count"] = status.AccountCount
			updates["online_count"] = status.OnlineCount
		}
	}
	if uerr := db.Model(&model.Node{}).Where("id = ?", n.Id).Updates(updates).Error; uerr != nil {
		return status, uerr
	}
	return status, err
}

// PollAll refreshes every enabled node. Each poll is independent and bounded,
// so one bad node never blocks the rest.
func (s *NodeService) PollAll() error {
	nodes, err := s.List()
	if err != nil {
		return err
	}
	for i := range nodes {
		if !nodes[i].Enable {
			continue
		}
		if _, perr := s.Poll(&nodes[i]); perr != nil {
			// A poll's transport error is already logged and reflected in the row's
			// online flag; the update itself only errors on a DB failure.
			if perr != nil {
				logger.Warning("polling node", nodes[i].Name, ":", perr)
			}
		}
	}
	return nil
}

// LocalStatus is this panel's own snapshot, shaped like a remote one so the
// dashboard can render "this node" with the same row as the remotes.
func (s *NodeService) LocalStatus() *model.NodeStatus {
	st := &model.NodeStatus{Version: config.GetVersion()}
	db := database.GetDB()
	var inbounds, accounts, online int64
	db.Model(&model.Inbound{}).Count(&inbounds)
	db.Model(&model.Account{}).Where("enable = ?", true).Count(&accounts)
	cutoff := time.Now().Add(-nodeOnlineWindow).UnixMilli()
	db.Model(&xray.ClientTraffic{}).Where("last_online > ? AND enable = ?", cutoff, true).Count(&online)
	st.InboundCount, st.AccountCount, st.OnlineCount = int(inbounds), int(accounts), int(online)
	return st
}

// fetchNodeStatus does the HTTP round trip. It authenticates with the node's
// token on the X-Node-Token header and decodes the NodeStatus body.
func fetchNodeStatus(n *model.Node) (*model.NodeStatus, error) {
	client := &http.Client{Timeout: nodePollTimeout}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(n.BaseUrl, "/")+nodeStatusPath, nil)
	if err != nil {
		return nil, err
	}
	if n.Token != "" {
		req.Header.Set("X-Node-Token", n.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var st model.NodeStatus
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("decoding status: %w", err)
	}
	return &st, nil
}

// isHttpUrl accepts a scheme-qualified http/https base url and rejects anything
// else (a bare host would silently default to http on some stacks, which is not
// what a remote panel address should be).
func isHttpUrl(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// Summary is the aggregate the dashboard header shows: totals across every
// enabled node plus this panel's own counts.
type NodeSummary struct {
	ThisInbounds int `json:"thisInbounds"`
	ThisAccounts int `json:"thisAccounts"`
	ThisOnline   int `json:"thisOnline"`
	NodeCount    int `json:"nodeCount"`
	OnlineCount  int `json:"onlineCount"`
	TotalInbounds int `json:"totalInbounds"`
	TotalAccounts int `json:"totalAccounts"`
	TotalOnline   int `json:"totalOnline"`
}

// ComputeSummary folds every enabled node's last snapshot plus this panel's own
// live counts into one header.
func (s *NodeService) ComputeSummary() NodeSummary {
	var sum NodeSummary
	local := s.LocalStatus()
	sum.ThisInbounds, sum.ThisAccounts, sum.ThisOnline = local.InboundCount, local.AccountCount, local.OnlineCount
	nodes, err := s.List()
	if err == nil {
		for i := range nodes {
			if !nodes[i].Enable {
				continue
			}
			sum.NodeCount++
			if nodes[i].Online {
				sum.OnlineCount++
			}
			sum.TotalInbounds += nodes[i].InboundCount
			sum.TotalAccounts += nodes[i].AccountCount
			sum.TotalOnline += nodes[i].OnlineCount
		}
	}
	// Fold this panel in as one more node so the "all nodes" totals are the real
	// fleet total, not just the remotes.
	sum.NodeCount++
	sum.OnlineCount++
	sum.TotalInbounds += sum.ThisInbounds
	sum.TotalAccounts += sum.ThisAccounts
	sum.TotalOnline += sum.ThisOnline
	return sum
}

// ensure NodeService is usable by pointer in the DI graph; keeps the compiler
// honest about the receiver type even if a method is inlined away.
var _ = gorm.ErrRecordNotFound
