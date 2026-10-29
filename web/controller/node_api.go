package controller

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// NodeAPIController is the REMOTE NODE's side of federation: the token-gated
// management surface a hub panel calls to list this panel's inbounds and to
// provision accounts on them. It deliberately lives OUTSIDE the session layer:
// the shared node API token is the transport credential, and every WRITE also
// needs this panel's operator to have approved the named hub (X-Hub-Name).
// Every mutation reuses the panel's own inbound service, so a node-created
// account is indistinguishable from one the local UI created — metered,
// live-applied and persisted identically.
type NodeAPIController struct {
	inboundService service.InboundService
	settingService service.SettingService
	nodeService    service.NodeService
}

// NewNodeAPIController wires the routes.
func NewNodeAPIController(g *gin.RouterGroup) *NodeAPIController {
	a := &NodeAPIController{
		inboundService: service.InboundService{},
		settingService: service.SettingService{},
		nodeService:    service.NodeService{},
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
	// Reads stay available to any holder of this panel's token: the hub uses them
	// to show what a node already serves while the pairing decision is pending.
	gp.GET("/inbounds", a.inbounds)
	gp.GET("/clients", a.clients)
	// Pairing: the connection handshake. A hub registers its request; the
	// panel's operator decides it on the Nodes page. A token alone is not enough
	// to WRITE here — the routes below are also gated by this panel's approval of
	// the named hub, so a node whose operator never said yes cannot be provisioned
	// into even by someone holding its token.
	gp.POST("/pairing/request", a.pairingRequest)
	gp.GET("/pairing/status", a.pairingStatus)

	writes := gp.Group("", a.approvalGate)
	writes.POST("/inbounds", a.createInbound)
	writes.POST("/clients", a.createClient)
	writes.POST("/delClient", a.deleteClient)
}

// approvalGate is the node-side half of the pairing handshake. The hub names
// itself on X-Hub-Name and the node only allows writes when its operator has
// approved that exact hub name. Missing or unknown names are refused: an old
// hub that predates the handshake has to ask for approval like every other one.
func (a *NodeAPIController) approvalGate(c *gin.Context) {
	hub := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Hub-Name")))
	if hub == "" {
		pureJsonMsg(c, http.StatusForbidden, false,
			"this node requires an approved hub identity (X-Hub-Name)")
		c.Abort()
		return
	}
	if a.nodeService.PairingStatusFor(hub) != service.PairingApproved {
		pureJsonMsg(c, http.StatusForbidden, false,
			"hub "+hub+" has not been approved by this node's operator")
		c.Abort()
		return
	}
	c.Next()
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

// pairingRequest is the node's side of the connection handshake: a hub panel
// registers itself for approval. The request is PENDING until this panel's
// operator decides it; a hub asking again after a decline reopens it.
func (a *NodeAPIController) pairingRequest(c *gin.Context) {
	var body struct {
		HubName string `json:"hubName" form:"hubName"`
		HubUrl  string `json:"hubUrl" form:"hubUrl"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	req, err := a.nodeService.UpsertPairingRequest(body.HubName, body.HubUrl)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"status": req.Status}, nil)
}

// pairingStatus is the node's answer to a hub's probe: its current decision
// for the named hub, "none" when it never asked.
func (a *NodeAPIController) pairingStatus(c *gin.Context) {
	status := a.nodeService.PairingStatusFor(c.Query("hub"))
	jsonObj(c, gin.H{"status": status}, nil)
}
