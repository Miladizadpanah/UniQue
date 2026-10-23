package controller

import (
	"crypto/subtle"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// NodeController owns the multi-node surface: the token-gated public status
// endpoint a remote panel exposes, and the session-gated /panel/api/nodes routes
// the hub uses to register and refresh its node registry. The hub only ever READS
// remote nodes; there is no write path to a remote panel through this controller.
type NodeController struct {
	nodeService    service.NodeService
	settingService service.SettingService
}

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
