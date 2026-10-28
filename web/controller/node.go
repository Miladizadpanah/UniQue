package controller

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
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
	nodes.POST("/:id/addClient", a.nodeAddClient)
	nodes.POST("/:id/delClient", a.nodeDelClient)
	// The hub's virtual accounts (NodeAccounts), each with the importable link
	// the customer gets.
	nodes.GET("/nodeAccounts", a.listNodeAccounts)
	nodes.POST("/nodeAccounts/del/:id", a.delNodeAccount)
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
	jsonObj(c, a.nodeService.LocalStatus(), nil)
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
	saved, err := a.nodeService.Save(&node)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
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
		InboundTag string     `json:"inboundTag" form:"inboundTag"`
		Protocol   string     `json:"protocol" form:"protocol"`
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

// nodeInbounds lists the node's enabled inbounds, settings blob included: the
// hub's UI shows them and snapshots one when it creates an account on it.
func (a *NodeController) nodeInbounds(c *gin.Context) {
	node, ok := a.nodeByID(c)
	if !ok {
		return
	}
	inbounds, err := a.nodeService.RemoteInbounds(node)
	jsonObj(c, inbounds, err)
}

// nodeAddClient provisions one account on a node inbound through the node's
// own panel API and records it here as a NodeAccount: the hub keeps the
// ownership row and the inbound snapshot, and from there the account rides the
// hub's subscriptions, pointing the customer's app at the node.
func (a *NodeController) nodeAddClient(c *gin.Context) {
	node, ok := a.nodeByID(c)
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
	if err := a.nodeService.DelNodeAccountByEmail(node.Id, body.Email); err != nil {
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
