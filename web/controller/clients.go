package controller

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/mhsanaei/3x-ui/v2/web/session"

	"github.com/gin-gonic/gin"
)

// clientGroupService owns the per-user client groups (see web/service/clientgroup.go).
var clientGroupService service.ClientGroupService

// inboundClientGroupService owns the inbound -> client-group scope and the auto-
// projection of a scoped inbound onto its groups' accounts (see web/service/
// inboundclientgroup.go). The group-members writes below call into it so a client
// that joins a group picks up every inbound that group is scoped to.
var inboundClientGroupService service.InboundClientGroupService

// The Clients page: the account-centric view of who exists, as opposed to the
// Inbounds page's view of what serves them.
//
// READ ONLY. Every mutation the page performs goes back through the existing
// client routes on /panel/api/inbounds (addClient, updateClient,
// delClientByEmail, bulkUpdateClients) carrying an inboundIds set. That is
// deliberate: those paths already carry the reseller pricing, the ownership
// assertions over every id, the same-protocol refusal and the projection, and a
// second write path would be a second place for all of that to drift out of step.
//
// The one account state those routes cannot express - an account NO inbound serves,
// which has no settings blob to be spliced into and no protocol to be addressed by -
// has two routes of its own, saveAccount and delAccount. They live on the same
// /panel/api/inbounds group and behind the same permission bits for exactly the
// reason above: the guards belong together, not on either side of a page boundary.
type ClientsController struct {
	BaseController
}

func NewClientsController(g *gin.RouterGroup) *ClientsController {
	a := &ClientsController{}
	a.initRouter(g)
	return a
}

func (a *ClientsController) initRouter(g *gin.RouterGroup) {
	// accessInbounds, the same claim the Inbounds page takes: this shows the same
	// accounts from the other side, so a caller who may see one may see the other.
	// The rows are then narrowed per caller inside ListAccounts.
	g.GET("/list", a.list)
	g.GET("/assignable", a.assignable)

	// The caller's per-user client groups, for organizing the client list. Reads
	// ride the group's access claim; the writes take the inbound-edit bit, matching
	// the inbound groups, because re-organizing the client list is structure work.
	g.GET("/groups", a.listClientGroups)
	g.POST("/groups", requirePerm(model.PermEditInbound), a.saveClientGroup)
	g.POST("/groups/del/:id", requirePerm(model.PermEditInbound), a.deleteClientGroup)
	g.POST("/groups/:id/members", requirePerm(model.PermEditInbound), a.setClientGroupMembers)
}

// list returns one page of accounts the caller may see.
//
// The scoping lives in the service and is the whole security surface here: the
// accounts and memberships tables are panel-wide, so an unscoped read would hand
// every admin every other admin's customers and every reseller every other
// seller's. Passing the login user rather than a flag keeps that decision in one
// place.
// The sort is a query parameter and not a body field because this is a GET, and it
// is validated in the service rather than here: an unknown key falls back to
// "newest" there, so there is nothing for this handler to reject. There is no
// direction parameter on purpose - each ordering the page offers already says which
// way it runs. The result echoes back the ordering that was actually applied.
func (a *ClientsController) list(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	size, _ := strconv.Atoi(c.Query("size"))
	// A group is a saved view the caller built over their own list; the filter
	// rides the same ownership check the service applies, so a foreign id is a
	// no-op rather than a leak.
	groupId, _ := strconv.Atoi(c.Query("group"))
	result, err := accountService.ListAccounts(session.GetLoginUser(c), page, size,
		c.Query("search"), c.Query("sort"), groupId)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
}

// assignable lists the inbounds the caller may put an account on, for the page's
// inbound picker.
//
// Filtered by the same grant the write path enforces, so the picker cannot offer
// an inbound the save would then refuse: an operator ticking a box and getting
// "not found" back with no explanation is the worst version of this.
func (a *ClientsController) assignable(c *gin.Context) {
	rows, err := accountService.AssignableInboundsFor(session.GetLoginUser(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, rows, nil)
}

// listClientGroups returns the caller's client groups, each with its members.
// The scoping (own groups only, super admin sees all) lives in the service.
func (a *ClientsController) listClientGroups(c *gin.Context) {
	groups, err := clientGroupService.List(session.GetLoginUser(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, groups, nil)
}

// saveClientGroup creates a group (id 0) or renames one (id > 0), then answers
// with the whole list: the panel renders the group panel as one unit, so the
// write is not a refetch.
func (a *ClientsController) saveClientGroup(c *gin.Context) {
	var body struct {
		Id   int    `form:"id" json:"id"`
		Name string `form:"name" json:"name"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	user := session.GetLoginUser(c)
	name := strings.TrimSpace(body.Name)
	var err error
	if body.Id > 0 {
		err = clientGroupService.Rename(user, body.Id, name)
	} else {
		_, err = clientGroupService.Create(user, name)
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	updated, err := clientGroupService.List(user)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, updated, nil)
}

// deleteClientGroup removes one group. Its clients are untouched - the service
// drops only the group row and its memberships. Before that, the inbounds scoped
// to the group are released from the accounts that had no other group claim to
// them (the "never disconnect on the last claim" rule), so deleting a group does
// not silently pull a scoped inbound away from a client still reached by another.
func (a *ClientsController) deleteClientGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	// Release the group's inbound scope first, while its membership rows still
	// exist for the drop logic to see. Capture the scoped inbounds to reconcile.
	scoped := inboundClientGroupService.InboundsForGroup(id)
	if len(scoped) > 0 {
		if gerr := inboundClientGroupService.DeleteGroupInboundLinks(id); gerr != nil {
			logger.Warning("releasing the deleted group's client-group inbound scope: ", gerr)
		}
	}
	if err := clientGroupService.Delete(session.GetLoginUser(c), id); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if len(scoped) > 0 {
		(&InboundController{}).reconcileForInbounds(scoped, true)
	}
	updated, err := clientGroupService.List(session.GetLoginUser(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, updated, nil)
}

// setClientGroupMembers replaces a group's membership with the posted ids. The
// ids arrive as a JSON array in a form field, matching the inbound groups: the
// panel posts form-urlencoded, which has no faithful encoding for an array.
func (a *ClientsController) setClientGroupMembers(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	var body struct {
		Data string `form:"data" json:"data"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	var ids []int
	if strings.TrimSpace(body.Data) != "" {
		if err := json.Unmarshal([]byte(body.Data), &ids); err != nil {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
			return
		}
	}
	user := session.GetLoginUser(c)
	// The accounts this change touches, so the inbound projection can be scoped to
	// exactly them: the old members (who may lose the group's inbounds) plus the new
	// ones (who gain them).
	oldMembers := inboundClientGroupService.MemberAccountIDs(id)
	if err := clientGroupService.SetMembers(user, id, ids); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	// A group now carries the inbounds scoped to it, so every account it gained or
	// lost has to re-project: gained accounts pick up those inbounds, lost accounts
	// drop an inbound only when no remaining group still claims it. Reconcile the
	// inbounds whose settings blobs changed so the running core and daemons match.
	affected := unionInboundIds(oldMembers, ids)
	var touched []int
	lost := []int{id} // the group this write edited: the members it no longer has lost it
	for _, accId := range affected {
		added, _ := inboundClientGroupService.ProjectAccountToGroupInbounds(accId)
		removed, _ := inboundClientGroupService.DropAccountFromGroupInbounds(accId, lost)
		touched = unionInboundIds(touched, added)
		touched = unionInboundIds(touched, removed)
	}
	if len(touched) > 0 {
		(&InboundController{}).reconcileForInbounds(touched, true)
	}
	updated, err := clientGroupService.List(user)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, updated, nil)
}
