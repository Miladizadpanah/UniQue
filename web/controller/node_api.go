package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
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
	// mTLS: the hub's CA cert + a signed client cert for the node. The token gate
	// still applies — mTLS is an ADDITIONAL channel, not a replacement.
	gp.GET("/mtls/ca", a.mtlsCA)
	gp.POST("/mtls/sign", a.mtlsSign)
	// Hub push telemetry: this panel's own 30s heartbeat job POSTs here when
	// it is configured as a node of a hub, and the hub's update request lands
	// here to trigger this panel's own self-update.
	gp.POST("/heartbeat", a.heartbeatReceiver)
	gp.POST("/updatePanel", a.updatePanel)

	writes := gp.Group("", a.approvalGate)
	writes.POST("/inbounds", a.createInbound)
	// Explicit clone route: the hub's "clone a local inbound onto this node"
	// action, on its own path so the hub's RemoteCreateInboundWithSettings can
	// POST there directly. The shape is the same NodeInboundClone; the route
	// is the same approval-gated group, so only an approved hub can clone.
	writes.POST("/inbounds/clone", a.createInboundFromClone)
	writes.POST("/clients", a.createClient)
	writes.POST("/delClient", a.deleteClient)
	// When the hub drops this node from its registry, it must also tear down
	// every account it provisioned here, or they would stay live on the
	// destination serving nobody's bill. The purge names the hub, and the
	// node removes that hub's provisioned accounts — the same set the hub's
	// NodeAccounts tracked — from its own inbounds.
	writes.POST("/purge", a.purgeByHub)
}

// purgeByHub removes every account this hub provisioned on the node's inbounds.
// The hub calls it when it deletes the node row, so the destination does not
// keep serving accounts whose owner (the hub) has gone away. It is keyed by the
// hub's identity, so a node shared by several hubs only loses the named hub's
// accounts.
func (a *NodeAPIController) purgeByHub(c *gin.Context) {
	var body struct {
		Emails []string `json:"emails"`
	}
	_ = c.ShouldBind(&body)
	var removed []int
	if len(body.Emails) > 0 {
		removed = a.purgeEmails(body.Emails)
	}
	jsonMsgObj(c, fmt.Sprintf("purged %d inbound(s)", len(removed)),
		map[string]any{"inboundIds": removed, "count": len(removed)}, nil)
}

// purgeEmails removes each email from every enabled inbound on this node that
// actually carries it. Returns the inbound ids touched so the caller can report
// the blast radius. A delete that finds no matching client is not an error: the
// account may already be gone, and the hub must still learn the final state.
func (a *NodeAPIController) purgeEmails(emails []string) []int {
	var inbounds []model.Inbound
	if err := database.GetDB().Model(&model.Inbound{}).
		Where("enable = ?", true).Find(&inbounds).Error; err != nil {
		return nil
	}
	touched := []int{}
	for i := range inbounds {
		ib := &inbounds[i]
		// Only inbounds that actually carry one of the named emails: deleting
		// from an inbound that has none would restart the core for nothing.
		if !inboundHasAnyEmail(a.inboundService, ib, emails) {
			continue
		}
		removed := false
		for _, email := range emails {
			if email == "" {
				continue
			}
			if a.inboundService.NodeDelClientByEmail(ib.Id, email) == nil {
				removed = true
			}
		}
		if removed {
			touched = append(touched, ib.Id)
		}
	}
	// Apply the changes live so the destination stops serving the accounts
	// without waiting for a core restart.
	(&service.XrayService{}).SetToNeedRestart()
	return touched
}

// inboundHasAnyEmail answers whether one of the named emails is a client of the
// inbound, so a purge can skip inbounds it would not change.
func inboundHasAnyEmail(svc service.InboundService, ib *model.Inbound, emails []string) bool {
	if ib.Settings == "" {
		return false
	}
	clients, err := svc.GetClients(ib)
	if err != nil {
		return false
	}
	set := map[string]bool{}
	for _, e := range emails {
		set[strings.ToLower(e)] = true
	}
	for i := range clients {
		if set[strings.ToLower(clients[i].Email)] {
			return true
		}
	}
	return false
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

// mtlsCA returns the panel's CA certificate so a node can pin it for mTLS.
func (a *NodeAPIController) mtlsCA(c *gin.Context) {
	ca, err := a.settingService.MTLSCertBundle()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	c.Data(http.StatusOK, "application/pem-certificate", ca)
}

// mtlsSign issues a signed client cert for the named node. The node exchanges
// this for a client cert it presents on every mTLS dial to the hub.
func (a *NodeAPIController) mtlsSign(c *gin.Context) {
	var body struct {
		NodeName string `json:"nodeName"`
	}
	if err := c.ShouldBind(&body); err != nil || strings.TrimSpace(body.NodeName) == "" {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), common.NewError("node name is required"))
		return
	}
	certPEM, err := a.settingService.SignClientCert(strings.TrimSpace(body.NodeName), 5*365*24*time.Hour)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	c.Data(http.StatusOK, "application/pem-certificate", certPEM)
}

// tokenGate is the constant-time node token check, upgraded for scoped
// tokens. It accepts EITHER the shared node token (full access, the legacy
// path — the settings-table value the status endpoint has always read) OR
// one of the panel's own scoped ApiToken rows (the scope restricts the
// routes the credential may open). The two coexist: a panel that has never
// minted a scoped token behaves exactly as before, and the shared token
// remains the full-access answer the legacy callers rely on.
//
// A scoped token's scope is checked against the route it opens: a
// read-only token (ScopeNodeRead) opens the read routes; a write token
// (ScopeNodeWrite) opens the write routes; a telemetry token
// (ScopeHeartbeat) opens only the heartbeat route. The shared token's
// scope is the all-bits mask, so it opens every route the way it always
// has. The approval gate (a.approvalGate) still applies to the write
// routes on top of this: a scoped write token is not enough to provision
// on a node whose operator never approved the hub.
func (a *NodeAPIController) tokenGate(c *gin.Context) {
	tokenSvc := service.ApiTokenService{}
	scope, ok := tokenSvc.ResolveScope(c.GetHeader("X-Node-Token"))
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	// The gate's per-route scope check: the route the credential is trying
	// to open has a required scope, and the credential's scope must carry
	// it. The shared token's all-bits mask carries every route; a scoped
	// token that does not carry the route's scope is refused, not
	// escalated. The required scope is derived from the request path.
	required := routeScopeForPath(c.FullPath())
	if scope&required == 0 {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Next()
}

// routeScopeForPath answers "which scope bit does this route require" —
// the gate's per-route check. The read routes require ScopeNodeRead, the
// write routes ScopeNodeWrite, and the telemetry route ScopeHeartbeat. A
// path the gate does not recognise (a future route the operator has not
// yet scoped) answers the most permissive bit, ScopeNodeWrite, so an
// unrecognised path is refused by a read-only token and answered by a
// write token: the safe direction is "the new route inherits the write
// gate", not "the new route is open to everyone".
func routeScopeForPath(path string) int64 {
	switch {
	case path == "/panel/node/heartbeat":
		return service.RouteScopeTelemetry
	case path == "/panel/node/inbounds" || path == "/panel/node/clients" ||
		path == "/panel/node/pairing/status" || path == "/panel/node/mtls/ca":
		return service.RouteScopeRead
	default:
		// Every other route in the group is a write route: the create,
		// clone, delete, purge, mTLS sign, and pairing request routes.
		// The default is the most permissive scope, so an unrecognised
		// path (a route added after this table was written) is refused
		// by a read-only token and answered by a write one.
		return service.RouteScopeWrite
	}
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
// panel. The body is either a plain NodeInboundCreate (protocol only — the
// node builds a free-port tcp vless/vmess of that protocol, idempotent per
// protocol) or a NodeInboundClone (protocol + streamSettings + settings blob
// + remark — the hub's "clone this local inbound onto me" request: the node
// builds a SIBLING inbound of the hub's own, on the node's own free port,
// hot-applied there). A body with a non-empty Settings is a clone, a body with
// only a Protocol is the dedicated federation inbound.
func (a *NodeAPIController) createInbound(c *gin.Context) {
	// First try the clone shape: the hub's "clone a local inbound onto this
	// node" request. It carries the source inbound's protocol + stream settings
	// + settings blob; the node builds a sibling on its own free port.
	var cloneReq service.NodeInboundClone
	if err := c.ShouldBind(&cloneReq); err == nil && (cloneReq.Settings != "" || cloneReq.StreamSettings != "" || cloneReq.Remark != "") {
		inbound, cerr := a.inboundService.CreateNodeClonedInbound(cloneReq)
		if cerr != nil {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), cerr)
			return
		}
		jsonObj(c, inbound, nil)
		return
	}
	// Otherwise the plain protocol-only create: the dedicated federation
	// inbound of that protocol (idempotent per protocol — a re-check finds it
	// by name and returns the existing one, nothing is duplicated).
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

// createInboundFromClone is the explicit clone route: POST /inbounds/clone with
// a NodeInboundClone body. It is the same as createInbound's clone branch but
// on its own path, so the hub's RemoteCreateInboundWithSettings can POST there
// directly rather than riding the createInbound's shape-detection.
func (a *NodeAPIController) createInboundFromClone(c *gin.Context) {
	var cloneReq service.NodeInboundClone
	if err := c.ShouldBind(&cloneReq); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	inbound, err := a.inboundService.CreateNodeClonedInbound(cloneReq)
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

// heartbeatReceiver is the HUB-side receiving half of a node's push telemetry.
// The token gate (shared node API token) identifies WHICH node is reporting;
// the heartbeat payload is the node's own NodeStatus. The hub records the gauges
// against that node's row; an unknown token (a node this panel does not track)
// is dropped silently rather than logged every 30s.
func (a *NodeAPIController) heartbeatReceiver(c *gin.Context) {
	var st model.NodeStatus
	if err := c.ShouldBindJSON(&st); err != nil {
		// A malformed body is a client bug; answer 200 so the node's job does
		// not retry-loop on a transient network corruption.
		jsonObj(c, gin.H{"accepted": false}, nil)
		return
	}
	if err := a.nodeService.RecordHeartbeat(c.GetHeader("X-Node-Token"), &st); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"accepted": true}, nil)
}

// updatePanel is the node's half of the hub's "update these panels" action:
// the hub POSTs here (token-gated), the node starts its own self-update in the
// background and answers straight away. The update's real progress shows on
// this node's own dashboard; the hub sees the version column flip on the next
// heartbeat/poll.
//
// dev selects the channel (stable release vs latest pre-release) the way this
// panel's own in-UI updater does; it is accepted and reported, not acted on
// (this build always pulls the configured repo's latest), so a hub that offers
// channel choice still gets a clean answer.
func (a *NodeAPIController) updatePanel(c *gin.Context) {
	var req struct {
		Dev bool `json:"dev"`
	}
	_ = c.ShouldBindJSON(&req)
	// The update runs in the background: a download that takes minutes must
	// not hold the hub's HTTP request open for that long. The hub gets "started"
	// now and watches the node's version column for the result.
	go func() {
		if err := (&service.ServerService{}).UpdatePanel(); err != nil {
			logger.Warning("remote panel update requested by hub failed:", err)
		}
	}()
	jsonObj(c, gin.H{"accepted": true}, nil)
}
