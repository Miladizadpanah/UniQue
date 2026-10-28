package controller

import (
	"crypto/subtle"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// NodeAPIController is the REMOTE NODE's side of federation: the token-gated
// management surface a hub panel calls to list this panel's inbounds and to
// provision accounts on them. It deliberately lives OUTSIDE the session layer:
// the node API token is the only credential, exactly like /panel/node/status.
// Every mutation reuses the panel's own inbound service, so a node-created
// account is indistinguishable from one the local UI created — metered,
// live-applied and persisted identically.
type NodeAPIController struct {
	inboundService service.InboundService
	settingService service.SettingService
}

// NewNodeAPIController wires the routes.
func NewNodeAPIController(g *gin.RouterGroup) *NodeAPIController {
	a := &NodeAPIController{
		inboundService: service.InboundService{},
		settingService: service.SettingService{},
	}
	a.initRouter(g)
	return a
}

func (a *NodeAPIController) initRouter(g *gin.RouterGroup) {
	// The same gate as /panel/node/status: the shared node token is the only
	// credential; an unset token (the default) refuses everyone, which is the
	// safe default — a panel is not remotely manageable until its operator opts
	// in by setting the token.
	gp := g.Group("/panel/node", a.tokenGate)
	gp.GET("/inbounds", a.inbounds)
	gp.POST("/inbounds", a.createInbound)
	gp.GET("/clients", a.clients)
	gp.POST("/clients", a.createClient)
	gp.POST("/delClient", a.deleteClient)
}

// tokenGate is the constant-time node token check. Same policy as the status
// endpoint: unset token denies everyone, so rotation is a one-field operation.
func (a *NodeAPIController) tokenGate(c *gin.Context) {
	configured := a.settingService.GetNodeApiToken()
	if configured == "" {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(configured), []byte(c.GetHeader("X-Node-Token"))) != 1 {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Next()
}

// inbounds is the node's enabled inbounds, settings blob included: the hub
// snapshots one after creating an account there, and the blob is what its link
// generator reads the credential out of.
func (a *NodeAPIController) inbounds(c *gin.Context) {
	var inbounds []model.Inbound
	err := database.GetDB().Model(&model.Inbound{}).
		Where("enable = ?", true).
		Order("id asc").
		Find(&inbounds).Error
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, inbounds, nil)
}

// createInbound makes ONE dedicated federation inbound available on this
// panel: a free-port tcp vless or vmess, created here when absent (idempotent
// per protocol), enabled and hot-applied, so the hub can provision accounts on
// it the moment it returns. The caller (the hub) sends only the protocol; the
// node decides the port and the settings, and answers with the inbound to use.
func (a *NodeAPIController) createInbound(c *gin.Context) {
	var req service.NodeInboundCreate
	if err := c.ShouldBind(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	inbound, err := a.inboundService.CreateNodeFedInbound(req.Protocol)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, inbound, nil)
}

// clients lists one inbound's accounts with their metered usage.
func (a *NodeAPIController) clients(c *gin.Context) {
	id, err := strconv.Atoi(c.DefaultQuery("inboundId", "0"))
	if err != nil || id <= 0 {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), common.NewError("inboundId must be a positive integer"))
		return
	}
	infos, err := a.inboundService.NodeClientList(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, infos, nil)
}

// createClient provisions one account on this panel through the node API: the
// hub sends the email and the limits, this panel mints the credential and the
// account lands in its own database and running core. The response carries the
// created client AND the updated inbound so the hub can snapshot it.
func (a *NodeAPIController) createClient(c *gin.Context) {
	var req service.NodeClientCreate
	if err := c.ShouldBind(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	client, err := a.inboundService.NodeAddClient(req)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	inbound, err := a.inboundService.GetInbound(req.InboundId)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, service.NodeClientResult{Client: client, Inbound: *inbound}, nil)
}

// deleteClient removes one account from an inbound by email.
func (a *NodeAPIController) deleteClient(c *gin.Context) {
	var body struct {
		InboundId int    `json:"inboundId" form:"inboundId"`
		Email     string `json:"email" form:"email"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.inboundService.NodeDelClientByEmail(body.InboundId, body.Email); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, "", nil)
}
