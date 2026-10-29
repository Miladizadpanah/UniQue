package controller

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// NodeController owns the multi-node surface: the token-gated public status
// endpoint a remote panel exposes, and the session-gated /panel/api/nodes routes
// the hub uses to register and refresh its node registry. The hub READS remote
// nodes by default; the federation routes (test / inbounds / clients) act on a
// remote panel through its own token-gated API, and the NodeAccount routes
// manage the hub's virtual accounts hosted on those nodes.
type NodeController struct {
	nodeService    service.NodeService
	settingService service.SettingService
	// inboundService is used only to parse credentials out of node-inbound
	// snapshots when building links the customer can import.
	inboundService service.InboundService
}

// NodeAccountLinker computes the importable link for one hub-owned, node-hosted
// account. It is INJECTED at boot (main.go) because the link generators live
// in the subscription package, which this package cannot import: sub imports
// web, web wires this controller — an import cycle. Nil means "link
// unavailable" and the node-accounts routes answer without a link rather than
// failing the request.
var NodeAccountLinker func(na *model.NodeAccount) string

// nodeStatusPathPublic is the status route relative to the base-path group; the
// full URL is basePath + this. Kept local so the hub-side and node-side agree on
// the wire path from one place.
const nodeStatusPathPublic = "/panel/node/status"

// NewNodeController wires the routes. The status route is public (token-gated,
// not session-gated) so the hub can poll a remote panel with its shared token;
// the management routes are the normal authenticated API.
func NewNodeController(g *gin.RouterGroup) *NodeController {
	a := &NodeController{}
	a.initRouter(g)
	return a
}

func (a *NodeController) initRouter(g *gin.RouterGroup) {
	// Public, token-gated. Lives outside the session middleware so the hub can
	// call it without a panel login; the token is the only credential.
	g.GET("/panel/node/status", a.nodeStatus)

	// Deleting a client also removes the accounts this hub provisioned for it on
	// remote nodes. That is client management, not fleet configuration, so it
	// carries the client-delete permission and the same ownership check as the
	// local delete routes instead of the Xray-settings bit the Nodes API uses.
	g.POST("/panel/api/nodes/nodeMemberships/del/:email",
		requirePerm(model.PermDeleteClient), requireClientAccess(), a.delNodeMembershipsByEmail)

	// The hub's management API. Node management is core-configuration-class work,
	// so it takes the Xray settings bit, matching other fleet-wide settings.
	nodes := g.Group("/panel/api/nodes")
	nodes.Use(requirePerm(model.PermXraySettings))
	nodes.GET("/list", a.list)
	nodes.GET("/summary", a.summary)
	nodes.GET("/token", a.getToken)
	nodes.POST("/token", a.setToken)
	nodes.POST("/add", a.save)
	nodes.POST("/update/:id", a.save)
	nodes.POST("/del/:id", a.del)
	nodes.POST("/refresh", a.refresh)
	// Write path: act on a remote node's own xray core through its API socket.
	// These are the operations the dashboard's "manage this node" panel offers:
	// hot-add / hot-remove an account and pull live traffic. All are bounded and
	// best-effort; a node without an API configured returns the read-only error.
	nodes.POST("/:id/hotAdd", a.hotAdd)
	nodes.POST("/:id/hotRemove", a.hotRemove)
	nodes.POST("/:id/pullTraffic", a.pullTraffic)
	// Federation: act on a remote node's PANEL through its token-gated API, so
	// the hub can verify the connection, list the node's inbounds, and provision
	// accounts on them (which the hub then tracks as NodeAccounts and serves in
	// its own subscriptions, pointing the customer's app at the node).
	nodes.POST("/:id/test", a.test)
	nodes.GET("/:id/inbounds", a.nodeInbounds)
	nodes.POST("/:id/ensureInbounds", a.ensureInbounds)
	nodes.POST("/:id/addClient", a.nodeAddClient)
	nodes.POST("/:id/delClient", a.nodeDelClient)
	// The hub's virtual accounts (NodeAccounts), each with the importable link
	// the customer gets.
	nodes.GET("/nodeAccounts", a.listNodeAccounts)
	nodes.POST("/nodeAccounts/del/:id", a.delNodeAccount)
	// Pairing: the hub's half of the handshake (ask this node's operator to
	// approve), and THIS panel's incoming requests for its own operator to
	// decide.
	nodes.POST("/:id/requestPairing", a.requestPairing)
	nodes.GET("/pairings", a.listPairings)
	nodes.POST("/pairings/decide/:id", a.decidePairing)
	// The client form's remote groups: the inbounds an approved node serves,
	// and the attach/detach of one client's node-hosted accounts.
	nodes.GET("/nodeOptions", a.nodeOptions)
	nodes.GET("/nodeMemberships", a.listNodeMemberships)
	nodes.POST("/nodeMemberships", a.syncNodeMemberships)
	// Mint a fresh random token for THIS panel's node API, replacing the old one.
	nodes.POST("/token/generate", a.generateToken)
}

// nodeStatus is the remote node's side: report this panel's own snapshot, gated
// by the shared node token. An unset token (the default) refuses everyone, which
// is the safe default: a panel is not pollable until its operator opts in.
func (a *NodeController) nodeStatus(c *gin.Context) {
	configured := a.settingService.GetNodeApiToken()
	if configured == "" {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	sent := c.GetHeader("X-Node-Token")
	// Constant-time compare so a missing/guessed token does not time out faster
	// than a right one and leak whether the endpoint is live.
	if subtle.ConstantTimeCompare([]byte(configured), []byte(sent)) != 1 {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	jsonObj(c, a.localStatusWithBaseUrl(c), nil)
}

// localStatusWithBaseUrl is LocalStatus plus the panel's own public base URL,
// so a hub that only holds this panel's token can discover the URL to store on
// its row. The hub's Save reads it off the first poll and uses it as the
// BaseUrl; a blank here means "unspecified" and the hub keeps what it had.
func (a *NodeController) localStatusWithBaseUrl(c *gin.Context) *model.NodeStatus {
	st := a.nodeService.LocalStatus()
	if st != nil {
		st.BaseUrl = a.baseUrl(c)
	}
	return st
}

// list returns the whole registry with each node's last snapshot.
func (a *NodeController) list(c *gin.Context) {
	nodes, err := a.nodeService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, nodes, nil)
}

// summary is the dashboard header: this panel's live counts folded with every
// enabled remote's last snapshot.
func (a *NodeController) summary(c *gin.Context) {
	jsonObj(c, a.nodeService.ComputeSummary(), nil)
}

// save creates (on /add) or updates (on /update/:id) one node. The token field
// is the remote panel's node status token; it is stored as-is and only ever sent
// OUT to that remote node on the poll.
//
// A blank Base URL with a token present is the "auto-discovered" case: the hub
// asks the node for its own public address (the node's /panel/node/status
// reports it) and stores that, so the operator only has to type the node's
// name and token. A typed Base URL is trusted as-is: it may point at a domain
// front the panel does not itself serve.
func (a *NodeController) save(c *gin.Context) {
	var node model.Node
	if err := c.ShouldBind(&node); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if c.Param("id") != "" {
		id, perr := strconv.Atoi(c.Param("id"))
		if perr != nil {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
			return
		}
		node.Id = id
	}
	// Auto-discover the Base URL when the operator only gave a token: the node's
	// own status endpoint answers with the address the hub reached it on. Best
	// effort — a node that is briefly unreachable still saves, and the next
	// poll fills the URL in.
	if strings.TrimSpace(node.BaseUrl) == "" && strings.TrimSpace(node.Token) != "" {
		if discovered, derr := a.nodeService.DiscoverNodeBaseUrl(&node); derr == nil && discovered != "" {
			node.BaseUrl = discovered
		}
	}
	isNew := node.Id == 0
	saved, err := a.nodeService.Save(&node)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	// Adding a node with a token sends the pairing request straight away, so the
	// destination operator's incoming-requests list fills without a second click.
	// It is best-effort: an old or briefly unreachable node must not fail the
	// save, and the Nodes page still offers the explicit Request action. A node
	// that is already approved (a re-add under the same hub name) is provisioned
	// immediately, which is what keeps the add flow turnkey.
	if isNew && saved.Token != "" {
		status, perr := a.nodeService.RemotePairingRequest(saved, service.NodeHubIdentity(), a.baseUrl(c))
		if perr != nil {
			logger.Warning("pairing request for new node", saved.Name, ":", perr)
		} else if status == service.PairingApproved {
			if uerr := database.GetDB().Model(&model.Node{}).Where("id = ?", saved.Id).
				Update("pairing_approved", true).Error; uerr != nil {
				logger.Warning("recording pairing approval for node", saved.Name, ":", uerr)
			}
			if fresh, gerr := a.nodeService.Get(saved.Id); gerr == nil {
				saved = fresh
			}
			if _, ferr := a.nodeService.EnsureFedInbounds(saved); ferr != nil {
				logger.Warning("provisioning federation inbounds on", saved.Name, ":", ferr)
			}
		}
	}
	jsonObj(c, saved, nil)
}

// del removes one node from the registry.
func (a *NodeController) del(c *gin.Context) {
	id, perr := strconv.Atoi(c.Param("id"))
	if perr != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
		return
	}
	// Gather this node's hub-provisioned emails BEFORE deleting the row, so the
	// destination node can be told to drop exactly those accounts. Best effort:
	// a purge that fails (node offline, not paired) must not block the local
	// delete — the operator is still allowed to retire a node, and the
	// destination's own operator can clean up its strays separately.
	node, gerr := a.nodeService.Get(id)
	var purgeErr error
	if gerr == nil && node != nil && node.BaseUrl != "" {
		accounts, _ := a.nodeService.ListNodeAccounts(id, "")
		emails := make([]string, 0, len(accounts))
		for i := range accounts {
			if accounts[i].Email != "" {
				emails = append(emails, accounts[i].Email)
			}
		}
		if len(emails) > 0 {
			purgeErr = a.nodeService.RemotePurgeByHub(node, emails)
			if purgeErr != nil {
				logger.Warning("node delete: remote purge of", node.Name, "failed:", purgeErr)
			}
		}
	}
	if err := a.nodeService.Delete(id); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.inboundDeleteSuccess"), nil)
}

// refresh polls every enabled node now and returns the registry with fresh
// snapshots.
func (a *NodeController) refresh(c *gin.Context) {
	if err := a.nodeService.PollAll(); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	nodes, _ := a.nodeService.List()
	jsonObj(c, nodes, nil)
}

// nodeByID loads one registered node for the write-path handlers.
func (a *NodeController) nodeByID(c *gin.Context) (*model.Node, bool) {
	id, perr := strconv.Atoi(c.Param("id"))
	if perr != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
		return nil, false
	}
	nodes, err := a.nodeService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return nil, false
	}
	for i := range nodes {
		if nodes[i].Id == id {
			return &nodes[i], true
		}
	}
	jsonMsg(c, I18nWeb(c, "somethingWentWrong"), fmt.Errorf("node %d not found", id))
	return nil, false
}

// hotAdd adds one account to a remote node's inbound, live, via the node's xray
// core API. Body: {inboundTag, protocol, user:{email, id, ...}}.
func (a *NodeController) hotAdd(c *gin.Context) {
	node, ok := a.nodeByID(c)
	if !ok {
		return
	}
	var body struct {
		InboundTag string         `json:"inboundTag" form:"inboundTag"`
		Protocol   string         `json:"protocol" form:"protocol"`
		User       map[string]any `json:"user" form:"user"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.nodeService.HotAddClient(node, body.InboundTag, body.Protocol, body.User); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.hotAddOk"), nil)
}

// hotRemove removes one account (by email) from a remote node's inbound, live.
// Body: {inboundTag, email}.
func (a *NodeController) hotRemove(c *gin.Context) {
	node, ok := a.nodeByID(c)
	if !ok {
		return
	}
	var body struct {
		InboundTag string `json:"inboundTag" form:"inboundTag"`
		Email      string `json:"email" form:"email"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.nodeService.HotRemoveClient(node, body.InboundTag, body.Email); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.hotRemoveOk"), nil)
}

// pullTraffic returns the remote node's live core statistics so the dashboard can
// show real-time traffic without waiting for the poll cadence.
func (a *NodeController) pullTraffic(c *gin.Context) {
	node, ok := a.nodeByID(c)
	if !ok {
		return
	}
	traffic, clientTraffic, err := a.nodeService.PullTraffic(node)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"traffic": traffic, "clients": clientTraffic}, nil)
}

// getToken returns this panel's own node status token and the panel's public base
// URL (scheme://host + base path), the value the hub pastes into the remote
// node's BaseUrl field. The hub appends the fixed status path when polling.
func (a *NodeController) getToken(c *gin.Context) {
	jsonObj(c, gin.H{
		"token":   a.settingService.GetNodeApiToken(),
		"baseUrl": a.baseUrl(c),
	}, nil)
}

// setToken sets (or clears, when empty) this panel's node status token. An empty
// value disables the status endpoint (safe-closed).
func (a *NodeController) setToken(c *gin.Context) {
	var body struct {
		Token string `json:"token" form:"token"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.settingService.SetNodeApiToken(body.Token); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"baseUrl": a.baseUrl(c)}, nil)
}

// baseUrl is the absolute base of this panel as the requesting client reached it
// (scheme://host + base path). The status path is a fixed suffix the hub adds.
func (a *NodeController) baseUrl(c *gin.Context) string {
	r := c.Request
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	basePath, _ := a.settingService.GetBasePath()
	return scheme + "://" + r.Host + basePath
}

// ---------------------------------------------------------------------------
// Federation: the hub acting on a remote node's panel through the node's
// token-gated API — verify the connection, browse its inbounds, provision
// accounts on them, and track those accounts as NodeAccounts that ride this
// panel's subscriptions.
// ---------------------------------------------------------------------------

// test is the save-&-verify round trip for a node row: one poll plus one
// inbounds fetch, so a token or url typo fails loudly here instead of leaving
// a node that silently never comes online.
func (a *NodeController) test(c *gin.Context) {
	node, ok := a.nodeByID(c)
	if !ok {
		return
	}
	inboundCount, err := a.nodeService.TestNode(node)
	// Re-read the row so the response carries the FRESH snapshot the poll just
	// wrote (online flag, version, latency), not the pre-poll copy.
	if fresh, rerr := a.nodeService.Get(node.Id); rerr == nil {
		node = fresh
	}
	jsonObj(c, gin.H{
		"online":       node.Online,
		"version":      node.Version,
		"latencyMs":    node.LatencyMs,
		"inboundCount": inboundCount,
	}, err)
}

// requireApproved is the hub-side half of the pairing handshake: the
// federation write paths only run on a node whose operator APPROVED this
// panel's connection request. Status polling stays open — approval governs
// writing to the node, not watching it.
func (a *NodeController) requireApproved(c *gin.Context) (*model.Node, bool) {
	node, ok := a.nodeByID(c)
	if !ok {
		return nil, false
	}
	if !node.PairingApproved {
		jsonMsg(c, I18nWeb(c, "pages.nodes.pairingNotApproved"),
			common.NewError("node", node.Name, "has not approved this panel's connection request yet"))
		return nil, false
	}
	return node, true
}

// nodeInbounds lists the node's enabled inbounds, settings blob included: the
// hub's UI shows them and snapshots one when it creates an account on it.
func (a *NodeController) nodeInbounds(c *gin.Context) {
	node, ok := a.requireApproved(c)
	if !ok {
		return
	}
	inbounds, err := a.nodeService.RemoteInbounds(node)
	jsonObj(c, inbounds, err)
}

// ensureInbounds guarantees the node's dedicated federation inbounds — a tcp
// vless and a tcp vmess — exist on the NODE and are enabled, creating the
// missing ones there through its token-gated API (free port, tcp, no TLS,
// hot-applied). It returns the full inbound list so the UI's provision form
// can dial one immediately. Reading stays first: existing inbounds are used,
// never duplicated.
func (a *NodeController) ensureInbounds(c *gin.Context) {
	node, ok := a.requireApproved(c)
	if !ok {
		return
	}
	inbounds, err := a.nodeService.EnsureFedInbounds(node)
	jsonObj(c, inbounds, err)
}

// nodeAddClient provisions one account on a node inbound through the node's
// own panel API and records it here as a NodeAccount: the hub keeps the
// ownership row and the inbound snapshot, and from there the account rides the
// hub's subscriptions, pointing the customer's app at the node.
func (a *NodeController) nodeAddClient(c *gin.Context) {
	node, ok := a.requireApproved(c)
	if !ok {
		return
	}
	var body struct {
		service.NodeClientCreate
		SubId string `json:"subId" form:"subId"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	res, err := a.nodeService.RemoteCreateClient(node, body.NodeClientCreate)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	snapshot, err := json.Marshal(res.Inbound)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	na := &model.NodeAccount{
		NodeId:          node.Id,
		RemoteInboundId: body.InboundId,
		Email:           res.Client.Email,
		ClientId:        res.Client.ID,
		SubId:           body.SubId,
		InboundSnapshot: string(snapshot),
	}
	saved, err := a.nodeService.SaveNodeAccount(na)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	// The importable link the customer gets is built by the subscription layer
	// (injected at boot; it owns every protocol's link generator). When the
	// linker is unavailable (it is not, on a normal boot) the account is still
	// recorded and the link stays empty.
	link := ""
	if NodeAccountLinker != nil {
		link = NodeAccountLinker(saved)
	}
	jsonObj(c, gin.H{"client": res.Client, "nodeAccount": saved, "link": link}, nil)
}

// nodeDelClient removes one account from a node inbound. Best effort on
// purpose: the remote delete runs first, and the hub forgets its row even when
// the node is unreachable — the operator asked for the account gone, and a
// dead node must not block the bookkeeping.
func (a *NodeController) nodeDelClient(c *gin.Context) {
	node, ok := a.nodeByID(c)
	if !ok {
		return
	}
	var body struct {
		InboundId int    `json:"inboundId" form:"inboundId"`
		Email     string `json:"email" form:"email"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.nodeService.RemoteDeleteClient(node, body.InboundId, body.Email); err != nil {
		logger.Warning("remote client delete on node", node.Name, ":", err)
	}
	if err := a.nodeService.DelNodeAccountByRef(node.Id, body.InboundId, body.Email); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.nodeClientDelOk"), nil)
}

// listNodeAccounts returns the hub's virtual accounts (optionally scoped to a
// node and/or subscription), each with its importable link when the linker is
// available. The link is computed by the subscription layer (injected at
// boot): the controller package cannot import it without an import cycle.
func (a *NodeController) listNodeAccounts(c *gin.Context) {
	nodeId, _ := strconv.Atoi(c.DefaultQuery("nodeId", "0"))
	subId := c.Query("subId")
	accounts, err := a.nodeService.ListNodeAccounts(nodeId, subId)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	type accountWithLink struct {
		model.NodeAccount
		Link string `json:"link"`
	}
	out := make([]accountWithLink, 0, len(accounts))
	for i := range accounts {
		row := accountWithLink{NodeAccount: accounts[i]}
		if NodeAccountLinker != nil {
			row.Link = NodeAccountLinker(&accounts[i])
		}
		out = append(out, row)
	}
	jsonObj(c, out, nil)
}

// delNodeAccount drops one ownership record (the local side only; the
// /delClient route is the one that also removes the remote account).
func (a *NodeController) delNodeAccount(c *gin.Context) {
	id, perr := strconv.Atoi(c.Param("id"))
	if perr != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
		return
	}
	if err := a.nodeService.DelNodeAccount(id); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.nodeAccountDelOk"), nil)
}

// generateToken mints a fresh random token for this panel's node API,
// replacing whatever is stored. Hubs still holding the old token lose access
// until they re-paste the new one: rotation is an explicit, operator-chosen
// break, not an accident.
func (a *NodeController) generateToken(c *gin.Context) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	token := hex.EncodeToString(buf)
	if err := a.settingService.SetNodeApiToken(token); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"token": token, "baseUrl": a.baseUrl(c)}, nil)
}

// ---------------------------------------------------------------------------
// Pairing, hub side: ask the node's operator to approve this panel, and the
// operator's view of the requests THIS panel has received.
// ---------------------------------------------------------------------------

// requestPairing is the hub's ask: register this panel with the node for
// approval and report the state straight away when a decision already exists.
// A just-returned approval lands on the row now; the poll keeps it current
// from there (and flips it off if the node operator ever declines it).
func (a *NodeController) requestPairing(c *gin.Context) {
	node, ok := a.nodeByID(c)
	if !ok {
		return
	}
	status, err := a.nodeService.RemotePairingRequest(node, service.NodeHubIdentity(), a.baseUrl(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if status == service.PairingApproved {
		// The snapshot fields are owned by the poller (Save's update map
		// excludes them), so the approval flag gets its own single-column
		// write rather than a full save.
		if uerr := database.GetDB().Model(&model.Node{}).Where("id = ?", node.Id).Update("pairing_approved", true).Error; uerr != nil {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), uerr)
			return
		}
		// The approval is the unlock: make the dedicated pair present on the node
		// right now instead of waiting for the next poll. Reading comes first
		// inside EnsureFedInbounds, so existing inbounds are reused, not duplicated.
		node.PairingApproved = true
		if _, ferr := a.nodeService.EnsureFedInbounds(node); ferr != nil {
			logger.Warning("pairing approved but federation inbounds on", node.Name, "are not ready:", ferr)
		}
	}
	jsonObj(c, gin.H{"status": status}, nil)
}

// listPairings is this panel's incoming connection requests: what the operator
// still has to decide. Newest first, as stored.
func (a *NodeController) listPairings(c *gin.Context) {
	reqs, err := a.nodeService.ListPairings()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, reqs, nil)
}

// decidePairing is the node operator's call: approve (the two panels become a
// federation — the two dedicated inbounds are made present HERE, created when
// absent, so the hub can provision the moment it hears back) or decline.
func (a *NodeController) decidePairing(c *gin.Context) {
	id, perr := strconv.Atoi(c.Param("id"))
	if perr != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
		return
	}
	var body struct {
		Approve bool `json:"approve" form:"approve"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.nodeService.DecidePairing(id, body.Approve); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if body.Approve {
		for _, protocol := range []string{string(model.VLESS), string(model.VMESS)} {
			if _, ferr := a.inboundService.CreateNodeFedInbound(protocol); ferr != nil {
				logger.Warning("pairing approval: fed inbound", protocol, "not ready:", ferr)
			}
		}
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.pairingDecided"), nil)
}

// ---------------------------------------------------------------------------
// Node groups for the client's membership form.
// ---------------------------------------------------------------------------

// nodeOptions is the remote groups: per approved node, the inbounds it serves
// right now. The client form draws one "Node: <name>" block out of each.
func (a *NodeController) nodeOptions(c *gin.Context) {
	jsonObj(c, a.nodeService.NodeInboundOptions(), nil)
}

// listNodeMemberships is one client's current node-hosted memberships, as the
// form's "nodeId:remoteInboundId" keys — what the checkboxes pre-check.
func (a *NodeController) listNodeMemberships(c *gin.Context) {
	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		jsonObj(c, []string{}, nil)
		return
	}
	accounts, err := a.nodeService.ListNodeAccountsByEmail(email)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	refs := make([]string, 0, len(accounts))
	for i := range accounts {
		refs = append(refs, service.NodeRefKey(accounts[i].NodeId, accounts[i].RemoteInboundId))
	}
	jsonObj(c, refs, nil)
}

// syncNodeMemberships makes the client's node-hosted accounts match the form's
// ticks: adds create them on their node, removals drop them there and here.
// Runs after the local save, so a client can live on node inbounds only.
func (a *NodeController) syncNodeMemberships(c *gin.Context) {
	var body struct {
		Email      string                      `json:"email" form:"email"`
		SubId      string                      `json:"subId" form:"subId"`
		TotalGb    int64                       `json:"totalGb" form:"totalGb"`
		ExpiryTime int64                       `json:"expiryTime" form:"expiryTime"`
		Enable     *bool                       `json:"enable" form:"enable"`
		Refs       []service.NodeMembershipRef `json:"refs" form:"refs"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	enable := true
	if body.Enable != nil {
		enable = *body.Enable
	}
	if err := a.nodeService.SyncNodeMemberships(body.Email, body.SubId, body.TotalGb, body.ExpiryTime, enable, body.Refs); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.nodeMembershipsOk"), nil)
}

// delNodeMembershipsByEmail removes every remote account this hub owns for one
// client, the cleanup half of deleting the client. Rows whose node could not be
// reached are reported back and kept, so the operator can retry rather than
// orphan an account on a node they no longer track.
func (a *NodeController) delNodeMembershipsByEmail(c *gin.Context) {
	email := strings.TrimSpace(c.Param("email"))
	if email == "" {
		jsonObj(c, gin.H{"failed": []string{}}, nil)
		return
	}
	failed := a.nodeService.DeleteNodeMembershipsForEmail(email)
	if failed == nil {
		failed = []string{}
	}
	jsonObj(c, gin.H{"failed": failed}, nil)
}
