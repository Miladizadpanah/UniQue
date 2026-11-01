package controller

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// TunnelController owns the native tunnel engine's UI surface: the session-gated
// /panel/api/tunnels routes (list / add / update / del / enable / link-test /
// token-gen) and the /panel/tunnels page. The engine's live state (up/down/
// stalled, byte gauges) is the watchdog's job to write onto the rows; this
// controller only mutates the CONFIG and reads the table back.
type TunnelController struct {
	tunnelService service.TunnelService
}

// NewTunnelController wires the routes.
func NewTunnelController(g *gin.RouterGroup) *TunnelController {
	a := &TunnelController{tunnelService: service.TunnelService{}}
	a.initRouter(g)
	return a
}

func (a *TunnelController) initRouter(g *gin.RouterGroup) {
	// The page: the operator's tunnel list + wizard. It carries the Xray
	// settings bit, the same class as node management (fleet-wide config).
	g.GET("/panel/tunnels", requirePerm(model.PermXraySettings), a.page)

	// The management API.
	api := g.Group("/panel/api/tunnels")
	api.Use(requirePerm(model.PermXraySettings))
	api.GET("/list", a.list)
	api.POST("/add", a.save)
	api.POST("/update/:id", a.save)
	api.POST("/del/:id", a.del)
	api.POST("/enable/:id", a.enable)
	api.POST("/linkTest", a.linkTest)
	api.POST("/token/generate", a.generateToken)
}

// page renders the tunnel dashboard.
func (a *TunnelController) page(c *gin.Context) {
	html(c, "tunnels.html", "pages.tunnels.title", nil)
}

// list returns the whole table with each row's last engine status.
func (a *TunnelController) list(c *gin.Context) {
	rows, err := a.tunnelService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, rows, nil)
}

// save creates (on /add) or updates (on /update/:id) one tunnel.
func (a *TunnelController) save(c *gin.Context) {
	var t model.TunnelPair
	if err := c.ShouldBindJSON(&t); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if c.Param("id") != "" {
		id, perr := strconv.Atoi(c.Param("id"))
		if perr != nil {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
			return
		}
		t.Id = id
	}
	saved, err := a.tunnelService.Save(&t)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, saved, nil)
}

// del removes one tunnel and stops its engine handle.
func (a *TunnelController) del(c *gin.Context) {
	id, perr := strconv.Atoi(c.Param("id"))
	if perr != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
		return
	}
	if err := a.tunnelService.Delete(id); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.tunnels.deleted"), nil)
}

// enable flips one row's enable flag; the engine follows suit.
func (a *TunnelController) enable(c *gin.Context) {
	id, perr := strconv.Atoi(c.Param("id"))
	if perr != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), perr)
		return
	}
	var body struct {
		Enable bool `json:"enable" form:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.tunnelService.SetEnable(id, body.Enable); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.tunnels.enabled"), nil)
}

// linkTest is the wizard's "which carrier for this route?" probe: one bounded
// dial against the named target, reporting latency, reachability, and the
// recommended transport. It is a READ of the path, not a tunnel mutation.
func (a *TunnelController) linkTest(c *gin.Context) {
	var body struct {
		Host string `json:"host" form:"host"`
		Port int    `json:"port" form:"port"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	res := a.tunnelService.LinkTest(body.Host, body.Port)
	jsonObj(c, res, nil)
}

// generateToken mints a fresh 64-hex-char secret, the same shape a tunnel's
// token field holds. The operator copies it to BOTH ends.
func (a *TunnelController) generateToken(c *gin.Context) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	token := hex.EncodeToString(buf)
	jsonObj(c, gin.H{"token": token}, nil)
}

// ensure the http import is live for a future 404 answer; keep the build
// green when the route set shrinks in a later phase.
var _ = http.StatusOK
var _ = fmt.Sprintf

