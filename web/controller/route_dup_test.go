package controller

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestXUIControllerBuild guards against the regression where two controllers
// both register the same path. Previously XUIController.initRouter registered
// GET /tunnel (page) AND NewTunnelController registered GET "" on its /tunnel
// group — same final path, gin panics at router construction with
// "handlers are already registered", killing the whole panel at startup
// (this is exactly what crashed the live server under v2.1.0).
//
// Building the controller tree here either succeeds (single registration) or
// panics the test (duplicate) — a duplicate is detected by gin at addRoute
// time, which is precisely the crash path.
func TestXUIControllerBuild(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	NewXUIController(engine.Group("/panel"))
}
