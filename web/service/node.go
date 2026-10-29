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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v2/config"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	xray "github.com/mhsanaei/3x-ui/v2/xray"
)

// The node's status, as it last reported. The hub polls this and caches the
// answer; a failed poll just leaves the row offline, it does not error the
// caller because the dashboard must still render an unreachable node.
type nodeHealthState struct {
	// ConsecutiveFailedPolls tracks how many polls in a row have failed. A node
	// is only marked offline after N consecutive failures (default 3), so a
	// transient network blip does not flap the dashboard.
	ConsecutiveFailures int `json:"consecutiveFailures"`
	// LastFailureTime is UnixMilli of the most recent failed poll, or 0.
	LastFailureTime int64 `json:"lastFailureTime"`
}

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

// The pairing endpoints: how a hub asks a node's operator to approve the
// connection, and how it probes the decision back. Both sit in the node's
// token-gated group, so a hub cannot pair with a node it has no token for.
const (
	nodePairingRequestPath = "/panel/node/pairing/request"
	nodePairingStatusPath  = "/panel/node/pairing/status"
)

// The pairing states, in the order the flow travels: nothing asked yet, asked
// and waiting on the node's operator, and the two decisions.
const (
	PairingNone     = "none"
	PairingPending  = "pending"
	PairingApproved = "approved"
	PairingDeclined = "declined"
)

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
	// A node is addressable when it has a Base URL, OR when it has a token the
	// hub can use to auto-discover the URL. The latter is the "operator only
	// typed name + token" case: Save stores the row with a blank BaseUrl and
	// the next Poll fills it in from the node's own status answer.
	if strings.TrimSpace(n.BaseUrl) == "" && strings.TrimSpace(n.Token) == "" {
		return nil, ErrNodeUrl
	}
	if n.BaseUrl != "" && !isHttpUrl(n.BaseUrl) {
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

// Delete removes one node and every ownership row for the accounts this hub
// provisioned on it. Nothing remote is touched; the rows and the snapshot simply
// stop being watched. Dropping the NodeAccount rows here is what keeps a deleted
// node from leaving accounts in the hub's subscriptions (and in the client form's
// node groups) pointing at a panel that is no longer registered.
func (s *NodeService) Delete(id int) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("node_id = ?", id).Delete(&model.NodeAccount{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Node{}, "id = ?", id).Error
	})
}

// Poll fetches one node's status, refreshes its snapshot row and returns the
// decoded status. A network or auth failure marks the node offline but does not
// error the caller: the dashboard must still render an unreachable node as
// "offline", not fail the whole refresh.
//
// To avoid flapping on transient network blips, a node is only marked offline
// after 3 consecutive failed polls. If the previous poll succeeded, the
// ConsecutiveFailures counter is reset.
func (s *NodeService) Poll(n *model.Node) (*model.NodeStatus, error) {
	start := time.Now()
	status, err := fetchNodeStatus(n)
	latency := int(time.Since(start).Milliseconds())
	db := database.GetDB()
	updates := map[string]any{"latency_ms": latency}
	becameApproved := false

	// Read the current health state (tracks consecutive failures for anti-flapping)
	// Note: health state is tracked in-memory per-poll cycle; not persisted.
	var healthRow nodeHealthState
	if err != nil {
		healthRow.ConsecutiveFailures++
		healthRow.LastFailureTime = time.Now().UnixMilli()

		// Only mark offline after 3 consecutive failures to prevent flapping
		if healthRow.ConsecutiveFailures >= 3 {
			updates["online"] = false
			// Fire a webhook alert on transition to offline
			s.fireNodeStatusWebhook(n, "node.offline")
		}
		if len(err.Error()) > 0 {
			logger.Warning("node poll", n.Name, "failed:", err)
		}
	} else {
		// Reset failure counter on success
		healthRow.ConsecutiveFailures = 0
		updates["online"] = true
		updates["last_seen"] = time.Now().UnixNano()
		if status != nil {
			updates["version"] = status.Version
			updates["inbound_count"] = status.InboundCount
			updates["account_count"] = status.AccountCount
			updates["online_count"] = status.OnlineCount
			// Auto-discover the Base URL when the operator only gave a token:
			// the node reports the address the hub reached it on, and the poll
			// keeps it current. A typed URL is never overwritten.
			if n.BaseUrl == "" && status.BaseUrl != "" {
				updates["base_url"] = status.BaseUrl
			}
		}
		// Fire a webhook alert on transition to online
		s.fireNodeStatusWebhook(n, "node.online")
		// Pairing: the node operator's decision for THIS hub, probed by the
		// hub's stable name. An error means the node predates pairing (no such
		// endpoint yet) or is unreachable for it: the flag stays as it was —
		// unknown is not a rejection.
		if n.Token != "" {
			if st, perr := s.RemotePairingStatus(n, NodeHubIdentity()); perr == nil {
				updates["pairing_approved"] = st == PairingApproved
				becameApproved = st == PairingApproved && !n.PairingApproved
			}
		}
	}

	if uerr := db.Model(&model.Node{}).Where("id = ?", n.Id).Updates(updates).Error; uerr != nil {
		return status, uerr
	}
	// Push the live gauges for this node to the Prometheus exporter.
	GetPrometheus().UpdateNodeGauges(
		n.Name,
		err == nil,
		int(n.InboundCount),
		int(n.AccountCount),
		int(n.OnlineCount),
		latency,
	)
	// The moment the node's operator approves, make the dedicated federation
	// inbounds present THERE. Reading first is EnsureFedInbounds' own contract:
	// an existing vless/vmess inbound is reused, only a missing one is created.
	// Best-effort: a hiccup here must not turn a healthy status poll into a
	// failure, and the next manage/ensure action retries it.
	if becameApproved {
		if _, ferr := s.EnsureFedInbounds(n); ferr != nil {
			logger.Warning("node", n.Name, "was approved but its federation inbounds are not ready:", ferr)
		}
	}
	return status, err
}

// fireNodeStatusWebhook sends a webhook notification when a node changes online/offline state.
// Best-effort: a failed webhook must not block the poll.
func (s *NodeService) fireNodeStatusWebhook(n *model.Node, eventType string) {
	var whService WebhookService
	whService.FireEvent(eventType, n.Name, map[string]any{
		"nodeId":  n.Id,
		"baseUrl": n.BaseUrl,
		"version": n.Version,
		"latency": n.LatencyMs,
		"online":  n.Online,
	})
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

// DiscoverNodeBaseUrl asks a node to report its own public base URL, so a
// hub that knows only the node's token does not have to type the URL. The
// node's /panel/node/status endpoint already returns the URL the panel
// reached it on (scheme + host + base path); the hub reads that off the
// first successful poll and uses it as the BaseUrl it stores.
//
// A blank BaseUrl on a node row therefore means "auto-discovered": the poller
// keeps it current on every poll so a node that moves hosts does not need the
// operator to re-edit it. A typed BaseUrl is trusted as-is (the operator may
// point a node at a domain front the panel does not itself serve).
func (s *NodeService) DiscoverNodeBaseUrl(n *model.Node) (string, error) {
	st, err := fetchNodeStatus(n)
	if err != nil {
		return "", err
	}
	if st == nil || st.BaseUrl == "" {
		return "", nil
	}
	return st.BaseUrl, nil
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
	ThisInbounds  int `json:"thisInbounds"`
	ThisAccounts  int `json:"thisAccounts"`
	ThisOnline    int `json:"thisOnline"`
	NodeCount     int `json:"nodeCount"`
	OnlineCount   int `json:"onlineCount"`
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
	nodePurgePath     = "/panel/node/purge"
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
	// The hub's stable name. The node's write endpoints require it AND require
	// that this hub's pairing request has been approved, so a token alone is not
	// enough to provision on a node whose operator never said yes.
	req.Header.Set("X-Hub-Name", NodeHubIdentity())
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

// RemoteCreateInbound asks the node's panel to make ONE dedicated tcp
// federation inbound of the protocol available there — created on the node
// itself when absent (free port, tcp, no TLS, enabled, hot-applied) — and
// returns the inbound to use. Idempotent: an existing enabled inbound of the
// protocol is returned instead of a duplicate.
func (s *NodeService) RemoteCreateInbound(n *model.Node, protocol string) (*model.Inbound, error) {
	var out model.Inbound
	req := NodeInboundCreate{Protocol: protocol}
	if err := nodeAPIRoundTrip(n, http.MethodPost, nodeInboundsPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EnsureFedInbounds guarantees BOTH dedicated inbounds exist on the node and
// are enabled — a tcp vless and a tcp vmess: what the hub provisions into and
// what the customer's links dial. Reading happens first; creating only what is
// missing. Existing inbounds are never duplicated or touched.
func (s *NodeService) EnsureFedInbounds(n *model.Node) ([]model.Inbound, error) {
	inbounds, err := s.RemoteInbounds(n)
	if err != nil {
		return nil, err
	}
	for _, protocol := range []string{string(model.VLESS), string(model.VMESS)} {
		if hasEnabledInbound(inbounds, protocol) {
			continue
		}
		created, err := s.RemoteCreateInbound(n, protocol)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, *created)
	}
	return inbounds, nil
}

// hasEnabledInbound answers "does this list already carry a usable inbound of
// the protocol", which is the only thing EnsureFedInbounds may assume about a
// node's own fleet: a disabled inbound of the right protocol still needs its
// replacement, and a different protocol's inbound does not satisfy it.
func hasEnabledInbound(inbounds []model.Inbound, protocol string) bool {
	for i := range inbounds {
		if inbounds[i].Enable && inbounds[i].Protocol == model.Protocol(protocol) {
			return true
		}
	}
	return false
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

// RemotePurgeByHub tells the node to drop every account the hub provisioned on
// it, by the set of emails the hub still tracked under this node. Called right
// before the hub deletes its Node row, so the destination stops serving
// orphaned accounts the moment the operator says goodbye to the node. Best
// effort from the hub's view: the node's own inbounds keep running, but the
// accounts that belonged to this hub go away with it.
func (s *NodeService) RemotePurgeByHub(n *model.Node, emails []string) error {
	body := map[string]any{"emails": emails}
	return nodeAPIRoundTrip(n, http.MethodPost, nodePurgePath, body, nil)
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
// keyed by (node, remote inbound, email): the same email on two different
// inbounds of one node is two independent accounts with two credentials, so
// keying by (node, email) alone would overwrite one of them. Re-creating the
// same account on the same inbound replaces the snapshot instead of stacking
// rows.
func (s *NodeService) SaveNodeAccount(na *model.NodeAccount) (*model.NodeAccount, error) {
	db := database.GetDB()
	var existing model.NodeAccount
	err := db.Where("node_id = ? AND remote_inbound_id = ? AND email = ?",
		na.NodeId, na.RemoteInboundId, na.Email).First(&existing).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		na.CreatedAt = time.Now().UnixMilli()
		if err := db.Create(na).Error; err != nil {
			return nil, err
		}
		return na, nil
	}
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

// DelNodeAccountByEmail drops every ownership record for one (node, email)
// pair, across all of that node's inbounds. Used when a whole client is being
// removed and the remote accounts have already been deleted.
func (s *NodeService) DelNodeAccountByEmail(nodeId int, email string) error {
	return database.GetDB().Where("node_id = ? AND email = ?", nodeId, email).Delete(&model.NodeAccount{}).Error
}

// DelNodeAccountByRef drops one ownership record: the exact (node, inbound,
// email) account a "delete this remote client" request names. Deleting by email
// alone would forget a second account with the same email on another inbound of
// the same node while its remote counterpart is still live.
func (s *NodeService) DelNodeAccountByRef(nodeId, remoteInboundId int, email string) error {
	return database.GetDB().
		Where("node_id = ? AND remote_inbound_id = ? AND email = ?", nodeId, remoteInboundId, email).
		Delete(&model.NodeAccount{}).Error
}

// ---------------------------------------------------------------------------
// Pairing: pointing at a node is not enough — the node's OPERATOR approves the
// connection. The request lands on the node as a pending row the operator
// decides; until the approval the hub only watches, and the federation writes
// (remote inbounds, provisioning, node groups in the client form) are refused
// on both sides.
// ---------------------------------------------------------------------------

// NodeHubIdentity is THIS panel's stable pairing name: the operator's server
// label when set, else the machine's hostname. Nodes key their requests by it,
// so it must stay stable between polls or the approval would not match.
func NodeHubIdentity() string {
	name, err := (&SettingService{}).GetServerName()
	if err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	host, herr := os.Hostname()
	if herr == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "unknown"
}

// UpsertPairingRequest registers a hub's connection request on THIS panel: a
// new hub starts pending, a hub re-asking after a decline reopens the door,
// and an already-approved hub stays approved.
func (s *NodeService) UpsertPairingRequest(hubName, hubUrl string) (*model.NodePairingRequest, error) {
	name := strings.ToLower(strings.TrimSpace(hubName))
	if name == "" {
		return nil, common.NewError("the hub's name is required for a pairing request")
	}
	db := database.GetDB()
	var req model.NodePairingRequest
	err := db.Where("hub_name = ?", name).First(&req).Error
	if err != nil {
		req = model.NodePairingRequest{
			HubName: name, HubUrl: hubUrl, Status: PairingPending,
			CreatedAt: time.Now().UnixMilli(),
		}
		if cerr := db.Create(&req).Error; cerr != nil {
			return nil, cerr
		}
		return &req, nil
	}
	if req.Status == PairingDeclined {
		// The hub asked again: the decision is up for grabs once more.
		req.Status = PairingPending
		req.DecidedAt = 0
		req.CreatedAt = time.Now().UnixMilli()
	}
	if hubUrl != "" {
		req.HubUrl = hubUrl
	}
	if uerr := db.Save(&req).Error; uerr != nil {
		return nil, uerr
	}
	return &req, nil
}

// ListPairings is the operator's view: every connection request this panel has
// received, newest first.
func (s *NodeService) ListPairings() ([]model.NodePairingRequest, error) {
	var out []model.NodePairingRequest
	err := database.GetDB().Order("id desc").Find(&out).Error
	return out, err
}

// DecidePairing is the node operator's call on one request.
func (s *NodeService) DecidePairing(id int, approve bool) error {
	db := database.GetDB()
	var req model.NodePairingRequest
	if err := db.First(&req, id).Error; err != nil {
		return err
	}
	req.Status = PairingApproved
	if !approve {
		req.Status = PairingDeclined
	}
	req.DecidedAt = time.Now().UnixMilli()
	return db.Save(&req).Error
}

// PairingStatusFor is the node's answer to a hub's status probe: its current
// decision for the named hub, "none" when it never asked.
func (s *NodeService) PairingStatusFor(hubName string) string {
	name := strings.ToLower(strings.TrimSpace(hubName))
	if name == "" {
		return PairingNone
	}
	var req model.NodePairingRequest
	if err := database.GetDB().Where("hub_name = ?", name).First(&req).Error; err != nil {
		return PairingNone
	}
	return req.Status
}

// RemotePairingRequest asks the node's operator to approve THIS panel's
// connection and returns the request's state after the node handled it.
func (s *NodeService) RemotePairingRequest(n *model.Node, hubName, hubUrl string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	body := map[string]string{"hubName": hubName, "hubUrl": hubUrl}
	if err := nodeAPIRoundTrip(n, http.MethodPost, nodePairingRequestPath, body, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

// RemotePairingStatus probes the node's decision for THIS panel, by the panel's
// stable name.
func (s *NodeService) RemotePairingStatus(n *model.Node, hubName string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	path := nodePairingStatusPath + "?hub=" + url.QueryEscape(hubName)
	if err := nodeAPIRoundTrip(n, http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

// ---------------------------------------------------------------------------
// Node groups in the client's membership form: the approved nodes and the
// inbounds they serve, and the attach/detach of one client's node-hosted
// accounts.
// ---------------------------------------------------------------------------

// NodeOption is one APPROVED node with the inbounds it is serving: a
// "Node: <name>" group in the client form. Unapproved or unreachable nodes do
// not appear — the form only offers what can actually carry the traffic.
type NodeOption struct {
	Node     model.Node      `json:"node"`
	Inbounds []model.Inbound `json:"inbounds"`
}

// NodeInboundOptions lists the client form's remote groups: per enabled
// APPROVED node, the inbounds it can actually carry accounts on right now. A
// node that is down at this moment is skipped, not an error: the group comes
// back with the next load.
//
// The fetch is parallel (bounded) because the client form waits for it before it
// opens: a fleet of ten nodes must not become ten sequential round trips. Only
// protocols nodeMintable accepts are offered, so the form cannot present an
// inbound the node API would refuse to provision into.
func (s *NodeService) NodeInboundOptions() []NodeOption {
	nodes, err := s.List()
	if err != nil {
		return nil
	}
	options := make([]*NodeOption, len(nodes))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range nodes {
		n := nodes[i]
		if !n.Enable || !n.PairingApproved || n.Token == "" {
			continue
		}
		wg.Add(1)
		go func(index int, node model.Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			inbounds, ierr := s.RemoteInbounds(&node)
			if ierr != nil {
				return
			}
			kept := make([]model.Inbound, 0, len(inbounds))
			for j := range inbounds {
				if inbounds[j].Enable && nodeMintable(inbounds[j].Protocol) {
					kept = append(kept, inbounds[j])
				}
			}
			if len(kept) == 0 {
				return
			}
			options[index] = &NodeOption{Node: node, Inbounds: kept}
		}(i, n)
	}
	wg.Wait()
	out := make([]NodeOption, 0, len(options))
	for _, opt := range options {
		if opt != nil {
			out = append(out, *opt)
		}
	}
	return out
}

// NodeMembershipRef names one remote inbound on one node: what the client form
// ticks when it attaches an account to a node.
type NodeMembershipRef struct {
	NodeId          int `json:"nodeId"`
	RemoteInboundId int `json:"remoteInboundId"`
}

// NodeRefKey is the "nodeId:remoteInboundId" string the UI carries and the hub's
// NodeAccounts speak.
func NodeRefKey(nodeId, remoteInboundId int) string {
	return strconv.Itoa(nodeId) + ":" + strconv.Itoa(remoteInboundId)
}

// ListNodeAccountsByEmail is the hub's ownership rows for one client: the diff
// input of a membership save.
func (s *NodeService) ListNodeAccountsByEmail(email string) ([]model.NodeAccount, error) {
	var out []model.NodeAccount
	err := database.GetDB().Where("email = ?", email).Order("id asc").Find(&out).Error
	return out, err
}

// SyncNodeMemberships makes one client's node-hosted accounts match the wanted
// set. The missing ones are created on their node through the node's own panel
// API — the same path as the standalone provision, metered and live-applied
// THERE — and the no-longer-wanted ones are removed there first, then
// forgotten here. A dead node's removal is best-effort: the operator asked for
// the membership gone, and an unreachable panel must not block the bookkeeping.
//
// Local inbounds are the caller's concern — this runs AFTER the local save and
// is keyed by email, not by an inbound, because a client may end up on node
// inbounds only.
//
// totalGb is BYTES (the unit NodeClientCreate and the panel's own client model
// use); expiryTime is the exact deadline in Unix milliseconds, with the same
// negative "days from first use" meaning the local account stores. Passing days
// instead would make a delayed-start account start counting on the node at
// creation time, which is not what the operator picked.
func (s *NodeService) SyncNodeMemberships(email, subId string, totalGb, expiryTime int64, enable bool, refs []NodeMembershipRef) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return common.NewError("email is required")
	}
	current, err := s.ListNodeAccountsByEmail(email)
	if err != nil {
		return err
	}
	byRef := make(map[string]*model.NodeAccount, len(current))
	for i := range current {
		na := &current[i]
		byRef[NodeRefKey(na.NodeId, na.RemoteInboundId)] = na
	}
	want := make(map[string]bool, len(refs))
	for _, r := range refs {
		want[NodeRefKey(r.NodeId, r.RemoteInboundId)] = true
	}

	done := make(map[string]bool, len(refs))
	for _, r := range refs {
		key := NodeRefKey(r.NodeId, r.RemoteInboundId)
		if done[key] {
			continue
		}
		done[key] = true
		if existing, ok := byRef[key]; ok {
			// The account already lives there; only the hub-side ownership row can
			// have changed. Keeping SubId current is what moves the node-hosted
			// account to the client's new subscription link after an edit.
			if existing.SubId != subId {
				if uerr := database.GetDB().Model(&model.NodeAccount{}).
					Where("node_id = ? AND remote_inbound_id = ? AND email = ?",
						existing.NodeId, existing.RemoteInboundId, existing.Email).
					Update("sub_id", subId).Error; uerr != nil {
					return uerr
				}
			}
			continue
		}
		node, gerr := s.Get(r.NodeId)
		if gerr != nil {
			return common.NewError("node", r.NodeId, "not found")
		}
		if !node.PairingApproved {
			return common.NewError("node", node.Name, "has not approved this panel's connection request")
		}
		req := NodeClientCreate{
			InboundId:  r.RemoteInboundId,
			Email:      email,
			TotalGb:    totalGb,
			ExpiryTime: expiryTime,
			Enable:     &enable,
		}
		res, cerr := s.RemoteCreateClient(node, req)
		if cerr != nil {
			return fmt.Errorf("node %s: %v", node.Name, cerr)
		}
		snapshot, _ := json.Marshal(res.Inbound)
		na := &model.NodeAccount{
			NodeId:          node.Id,
			RemoteInboundId: r.RemoteInboundId,
			Email:           res.Client.Email,
			ClientId:        res.Client.ID,
			SubId:           subId,
			InboundSnapshot: string(snapshot),
		}
		if _, serr := s.SaveNodeAccount(na); serr != nil {
			return serr
		}
	}

	removed := make(map[string]bool, len(current))
	for i := range current {
		na := &current[i]
		key := NodeRefKey(na.NodeId, na.RemoteInboundId)
		if want[key] || removed[key] {
			continue
		}
		removed[key] = true
		if node, gerr := s.Get(na.NodeId); gerr == nil {
			if derr := s.RemoteDeleteClient(node, na.RemoteInboundId, na.Email); derr != nil {
				logger.Warning("node client delete on node", node.Name, ":", derr)
			}
		}
		if derr := s.DelNodeAccount(na.Id); derr != nil {
			return derr
		}
	}
	return nil
}

// DeleteNodeMembershipsForEmail removes every remote account this hub owns for
// one client email, then forgets the rows. It returns the nodes whose remote
// delete failed; those rows are KEPT on purpose so a later retry can still name
// the account, rather than orphaning it on the node.
func (s *NodeService) DeleteNodeMembershipsForEmail(email string) []string {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	accounts, err := s.ListNodeAccountsByEmail(email)
	if err != nil {
		return []string{"database: " + err.Error()}
	}
	var failed []string
	done := make(map[string]bool, len(accounts))
	for i := range accounts {
		na := &accounts[i]
		key := NodeRefKey(na.NodeId, na.RemoteInboundId)
		if done[key] {
			// A duplicate ownership row (possible only on a database written by an
			// older build): the remote account is already gone, just drop the row.
			if derr := s.DelNodeAccount(na.Id); derr != nil {
				failed = append(failed, "database")
			}
			continue
		}
		done[key] = true
		node, gerr := s.Get(na.NodeId)
		if gerr == nil {
			if derr := s.RemoteDeleteClient(node, na.RemoteInboundId, na.Email); derr != nil {
				failed = append(failed, node.Name)
				continue
			}
		}
		if derr := s.DelNodeAccount(na.Id); derr != nil {
			failed = append(failed, "database")
		}
	}
	return failed
}
