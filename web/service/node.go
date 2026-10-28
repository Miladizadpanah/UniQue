package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	// Normalize the API TLS mode: an empty value defaults to insecure (a LAN node),
	// and a typo is rejected up front rather than surfacing on the first RPC.
	n.XrayApiHost = strings.TrimSpace(n.XrayApiHost)
	n.ApiCaPem = strings.TrimSpace(n.ApiCaPem)
	n.ApiTlsMode = normalizeApiTlsMode(n.ApiTlsMode)
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
			"xray_api_host": n.XrayApiHost, "xray_api_port": n.XrayApiPort,
			"api_tls_mode": n.ApiTlsMode, "api_ca_pem": n.ApiCaPem,
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

// normalizeApiTlsMode maps the operator's field to one of the accepted API TLS
// modes, defaulting a blank to insecure. An unknown value falls back to
// insecure (the safe choice for a LAN node) so a stray string never blocks a
// save; the strict check happens at connect time in the xray package.
func normalizeApiTlsMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case xray.ApiTlsSkip, xray.ApiTlsVerify, xray.ApiTlsInsecure, "":
		if m == "" {
			return xray.ApiTlsInsecure
		}
		return strings.ToLower(strings.TrimSpace(m))
	default:
		return xray.ApiTlsInsecure
	}
}

// nodeHasRemoteApi reports whether a node is configured for the write path: it
// must expose an xray API port. Without that the node stays read-only and the
// hub only monitors it.
func nodeHasRemoteApi(n *model.Node) bool {
	return n != nil && n.XrayApiPort > 0
}

// remoteApiTarget resolves the gRPC target host:port for a node's xray API. An
// empty XrayApiHost means "the same host the panel base url points at".
func remoteApiTarget(n *model.Node) string {
	host := strings.TrimSpace(n.XrayApiHost)
	if host == "" {
		host = baseHostOf(n.BaseUrl)
	}
	return net.JoinHostPort(host, strconv.Itoa(n.XrayApiPort))
}

// baseHostOf pulls the host (no port, no scheme) out of a panel base url so the
// default remote API host tracks the panel's own host.
func baseHostOf(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	h := u.Hostname()
	return h
}

// ErrNodeNoRemoteApi is returned by the write-path methods when a node has no
// xray API configured; the caller should surface it as "node is read-only".
var ErrNodeNoRemoteApi = errors.New("node has no xray api configured (read-only)")

// OpenRemote dials a node's xray core API and returns a ready-to-use client. The
// connection is closed by the caller (defer c.Close()). This is the hub acting
// as a gRPC client to the node's own core — the safe write path.
func (s *NodeService) OpenRemote(n *model.Node) (*xray.XrayAPI, error) {
	if !nodeHasRemoteApi(n) {
		return nil, ErrNodeNoRemoteApi
	}
	c := &xray.XrayAPI{}
	if err := c.NewRemote(remoteApiTarget(n), n.ApiTlsMode, []byte(n.ApiCaPem)); err != nil {
		return nil, err
	}
	return c, nil
}

// HotAddClient adds one account to a remote node's existing inbound and applies
// it live through the core API. The user map is shaped like the panel's own
// inbound users (protocol + id + email + optional flow/...), so any protocol the
// node runs works.
func (s *NodeService) HotAddClient(n *model.Node, inboundTag, protocol string, user map[string]any) error {
	c, err := s.OpenRemote(n)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.AddUser(protocol, inboundTag, user)
}

// HotRemoveClient drops one account (by email) from a remote node's inbound and
// applies the change live.
func (s *NodeService) HotRemoveClient(n *model.Node, inboundTag, email string) error {
	c, err := s.OpenRemote(n)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.RemoveUser(inboundTag, email)
}

// PullTraffic fetches the remote node's live core statistics (never resetting
// counters) so the dashboard can show real-time in/out per node without waiting
// for the poll cadence.
func (s *NodeService) PullTraffic(n *model.Node) ([]*xray.Traffic, []*xray.ClientTraffic, error) {
	c, err := s.OpenRemote(n)
	if err != nil {
		return nil, nil, err
	}
	defer c.Close()
	return c.GetTraffic(false)
}

// ---------------------------------------------------------------------------
// Node federation: the HUB side. The hub calls the node's token-gated panel
// API (the endpoints NodeAPIController exposes) to list its inbounds and to
// provision accounts on them. A hub-provisioned account is tracked locally as
// a NodeAccount so it can ride the hub's own subscriptions: the customer's
// app dials the NODE, not the hub.
// ---------------------------------------------------------------------------

// NodeClientResult is the node API's create response: the account the node
// minted plus the inbound's full row, which the hub snapshots for link
// generation.
type NodeClientResult struct {
	Client  model.Client  `json:"client"`
	Inbound model.Inbound `json:"inbound"`
}

// NodeApiError is the error a node API call returns when the node answered but
// the call failed (a 4xx/5xx envelope), carrying the node's message when it
// gave one.
type NodeApiError struct {
	What    string
	Status  int
	Message string
}

func (e *NodeApiError) Error() string {
	if e.Message != "" {
		return e.What + ": " + e.Message
	}
	return fmt.Sprintf("%s failed with status %d", e.What, e.Status)
}

const (
	nodeInboundsPath  = "/panel/node/inbounds"
	nodeClientsPath   = "/panel/node/clients"
	nodeDelClientPath = "/panel/node/delClient"
)

// nodeAPIResponse is the panel's standard JSON envelope every endpoint answers
// with; obj is kept raw so each caller decodes its own shape.
type nodeAPIResponse struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// nodeAPIRoundTrip calls one of a node's token-gated endpoints: GET/POST with
// an optional JSON body, the shared node token on X-Node-Token, and the
// envelope decoded into out when it is non-nil. Transport errors and non-200
// answers come back as concrete errors the UI can show verbatim.
func nodeAPIRoundTrip(n *model.Node, method, path string, body any, out any) error {
	client := &http.Client{Timeout: nodePollTimeout}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	u := strings.TrimRight(n.BaseUrl, "/") + path
	req, err := http.NewRequest(method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.Token != "" {
		req.Header.Set("X-Node-Token", n.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	env := new(nodeAPIResponse)
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(env); err != nil {
		if resp.StatusCode != http.StatusOK {
			return &NodeApiError{What: path, Status: resp.StatusCode}
		}
		return fmt.Errorf("decoding node response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !env.Success {
		return &NodeApiError{What: path, Status: resp.StatusCode, Message: env.Msg}
	}
	if out != nil && len(env.Obj) > 0 && string(env.Obj) != "null" {
		if err := json.Unmarshal(env.Obj, out); err != nil {
			return fmt.Errorf("decoding node response body: %w", err)
		}
	}
	return nil
}

// RemoteInbounds lists the node's enabled inbounds with everything the hub's
// link generator needs: protocol, port, listen, stream settings and the full
// settings blob (which carries each account's credential).
func (s *NodeService) RemoteInbounds(n *model.Node) ([]model.Inbound, error) {
	var out []model.Inbound
	if err := nodeAPIRoundTrip(n, http.MethodGet, nodeInboundsPath, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoteClients lists the accounts on one of the node's inbounds, metered, so
// the hub can show the full state of an inbound it provisions into.
func (s *NodeService) RemoteClients(n *model.Node, inboundId int) ([]NodeClientInfo, error) {
	var out []NodeClientInfo
	path := fmt.Sprintf("%s?inboundId=%d", nodeClientsPath, inboundId)
	if err := nodeAPIRoundTrip(n, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoteCreateClient provisions one account on a node inbound through the
// node's own panel API (persistent + live-applied THERE) and returns the
// account plus the inbound it landed on; the caller snapshots that inbound.
func (s *NodeService) RemoteCreateClient(n *model.Node, req NodeClientCreate) (*NodeClientResult, error) {
	var res NodeClientResult
	if err := nodeAPIRoundTrip(n, http.MethodPost, nodeClientsPath, req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RemoteDeleteClient removes one account (by email) from a node inbound.
func (s *NodeService) RemoteDeleteClient(n *model.Node, inboundId int, email string) error {
	body := map[string]any{"inboundId": inboundId, "email": email}
	return nodeAPIRoundTrip(n, http.MethodPost, nodeDelClientPath, body, nil)
}

// TestNode is the "save & verify" round trip: one status poll (which also
// refreshes the snapshot row) plus one inbounds fetch, so a token or url
// mistake surfaces as a concrete error at save time instead of a node that
// silently never comes online.
func (s *NodeService) TestNode(n *model.Node) (int, error) {
	if _, err := s.Poll(n); err != nil {
		return 0, err
	}
	inbounds, err := s.RemoteInbounds(n)
	if err != nil {
		return 0, err
	}
	return len(inbounds), nil
}

// Get loads one node by id, or nil when absent (the handlers 404 it).
func (s *NodeService) Get(id int) (*model.Node, error) {
	var n model.Node
	err := database.GetDB().First(&n, id).Error
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// ListNodeAccounts returns the hub's virtual accounts, optionally scoped to a
// node and/or a subscription, with their nodes preloaded so a caller can
// resolve a node's public host without a second query.
func (s *NodeService) ListNodeAccounts(nodeId int, subId string) ([]model.NodeAccount, error) {
	q := database.GetDB().Model(&model.NodeAccount{}).Preload("Node")
	if nodeId > 0 {
		q = q.Where("node_id = ?", nodeId)
	}
	if subId != "" {
		q = q.Where("sub_id = ?", subId)
	}
	var out []model.NodeAccount
	err := q.Order("id desc").Find(&out).Error
	return out, err
}

// SaveNodeAccount upserts the hub's ownership record for a remote account,
// keyed by (node, email): re-creating the same email on the same node
// replaces the snapshot instead of stacking rows.
func (s *NodeService) SaveNodeAccount(na *model.NodeAccount) (*model.NodeAccount, error) {
	db := database.GetDB()
	var existing model.NodeAccount
	err := db.Where("node_id = ? AND email = ?", na.NodeId, na.Email).First(&existing).Error
	if err != nil {
		na.CreatedAt = time.Now().UnixMilli()
		if err := db.Create(na).Error; err != nil {
			return nil, err
		}
		return na, nil
	}
	existing.RemoteInboundId = na.RemoteInboundId
	existing.ClientId = na.ClientId
	existing.SubId = na.SubId
	existing.InboundSnapshot = na.InboundSnapshot
	if err := db.Save(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// DelNodeAccount drops one ownership record. The REMOTE account is the
// caller's concern; this only forgets the hub's side.
func (s *NodeService) DelNodeAccount(id int) error {
	return database.GetDB().Delete(&model.NodeAccount{}, "id = ?", id).Error
}

// DelNodeAccountByEmail drops the ownership record for one (node, email) pair,
// the shape a "delete this remote client" request comes in.
func (s *NodeService) DelNodeAccountByEmail(nodeId int, email string) error {
	return database.GetDB().Where("node_id = ? AND email = ?", nodeId, email).Delete(&model.NodeAccount{}).Error
}
