package controller

import (
	"os"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/op/go-logging"
)

// initTestLogger points the file backend at a throwaway dir before the first
// log write. config.GetLogFolder defaults to /var/log/vpn-ui, which the CI
// runner (and most dev boxes) cannot create without root — that made the whole
// package's tests fail on a permission error, not on a real assertion. Pointing
// VPNUI_LOG_FOLDER at t.TempDir() keeps InitLogger happy on every platform.
func initTestLogger(t *testing.T) {
	t.Helper()
	if err := os.Setenv("VPNUI_LOG_FOLDER", t.TempDir()); err != nil {
		t.Fatalf("set VPNUI_LOG_FOLDER: %v", err)
	}
	logger.InitLogger(logging.CRITICAL)
}
