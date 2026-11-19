// Package config provides configuration management utilities for the vpn-ui panel,
// including version information, logging levels, database paths, and environment variable handling.
package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed version
var version string

//go:embed name
var name string

// LogLevel represents the logging level for the application.
type LogLevel string

// Logging level constants
const (
	Debug   LogLevel = "debug"
	Info    LogLevel = "info"
	Notice  LogLevel = "notice"
	Warning LogLevel = "warning"
	Error   LogLevel = "error"
)

// GetVersion returns the version string of the vpn-ui application.
func GetVersion() string {
	return strings.TrimSpace(version)
}

// panelRepoDefault is the GitHub owner/name the in-panel updater fetches release
// assets from, stamped at build time via -ldflags. A fork's build script stamps
// its own repo so the self-updater downloads from the fork; when it is left
// empty (upstream builds, dev builds, or a build that did not pass the flag)
// the updater falls back to the env override, then to the upstream repo.
var panelRepoDefault string

// GetPanelRepo answers which repo the in-panel updater should download the
// binary and release notes from:
//
//  1. the build-time -ldflags value (what a fork's release binary carries),
//  2. the VPNUI_REPO environment variable (an operator override on a running
//     host, no recompile),
//  3. the upstream default, so a never-stamped binary still works.
func GetPanelRepo() string {
	if r := strings.TrimSpace(panelRepoDefault); r != "" {
		return r
	}
	if r := strings.TrimSpace(os.Getenv("VPNUI_REPO")); r != "" {
		return r
	}
	return "Miladizadpanah/UniQue"
}

// GetName returns the name of the vpn-ui application.
func GetName() string {
	return strings.TrimSpace(name)
}

// assetVersion is the cache-busting token stamped on every asset URL. It lives
// here, set once at web-server start, because the two places that need it (the
// panel's template context and the subscription page's) both import config and
// neither can import web, which is where the embedded asset tree actually is.
//
// It is deliberately NOT GetVersion(). Assets are served with a one-year
// max-age, so the query string is the only thing that can make a browser fetch a
// new copy, and the release version does not move between builds of the same
// version. That combination silently pairs a rebuilt panel's NEW html with a
// browser's OLD javascript, which is not a degraded page but a blank one: the
// html calls a method the cached model file has not got, Vue throws mid-render
// and v-cloak leaves the area empty with nothing on screen to say why.
var assetVersion = strings.TrimSpace(version)

// SetAssetVersion records the token, which must be derived from the served asset
// bytes so that it changes exactly when they do.
func SetAssetVersion(v string) {
	if v != "" {
		assetVersion = v
	}
}

// GetAssetVersion returns the asset cache-busting token, falling back to the
// release version until the web server has computed one.
func GetAssetVersion() string {
	return assetVersion
}

// GetLogLevel returns the current logging level based on environment variables or defaults to Info.
func GetLogLevel() LogLevel {
	if IsDebug() {
		return Debug
	}
	logLevel := os.Getenv("VPNUI_LOG_LEVEL")
	if logLevel == "" {
		return Info
	}
	return LogLevel(logLevel)
}

// IsDebug returns true if debug mode is enabled via the VPNUI_DEBUG environment variable.
func IsDebug() bool {
	return os.Getenv("VPNUI_DEBUG") == "true"
}

// GetBinFolderPath returns the path to the binary folder, defaulting to "bin" if not set via VPNUI_BIN_FOLDER.
func GetBinFolderPath() string {
	binFolderPath := os.Getenv("VPNUI_BIN_FOLDER")
	if binFolderPath == "" {
		binFolderPath = "bin"
	}
	return binFolderPath
}

func getBaseDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	exeDir := filepath.Dir(exePath)
	exeDirLower := strings.ToLower(filepath.ToSlash(exeDir))
	if strings.Contains(exeDirLower, "/appdata/local/temp/") || strings.Contains(exeDirLower, "/go-build") {
		wd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return wd
	}
	return exeDir
}

// GetDBFolderPath returns the folder that holds the database file. It defaults to
// the directory of the binary (overridable with VPNUI_DB_FOLDER) so a copied or
// moved install carries its data with it, rather than silently sharing a fixed
// /etc/vpn-ui. Legacy installs are migrated from LegacyDBPath on first init.
func GetDBFolderPath() string {
	dbFolderPath := os.Getenv("VPNUI_DB_FOLDER")
	if dbFolderPath != "" {
		return dbFolderPath
	}
	return getBaseDir()
}

// dbBaseName is the database file's base name (without extension). It is fixed
// rather than derived from GetName() so the on-disk DB is always "vpn-ui.db".
const dbBaseName = "vpn-ui"

// GetDBPath returns the full path to the database file (next to the binary).
func GetDBPath() string {
	return fmt.Sprintf("%s/%s.db", GetDBFolderPath(), dbBaseName)
}

// LegacyDBPaths lists the database files an upgrade may have left behind, in the
// order they are preferred when more than one exists.
//
// It covers more than "next to the binary" on purpose. The panel's DB lives next
// to the executable, so ANY change to where the executable is installed — a
// deploy.sh update from an older layout, a move to /opt/vpn-ui, an operator
// dropping the binary somewhere new — silently starts the new binary on a fresh,
// empty database next to itself. The operator then sees an empty panel, zero
// inbounds and none of their clients, with no error anywhere. Discovered-but-
// non-empty databases from the well-known locations below are therefore adopted
// (see database.migrateLegacyDB), and the empty file the panel had just created
// is kept aside rather than deleted.
//
// Order matters: the binary's own directory and its parent first, then the
// historical install prefixes, then /root where a hand-run binary often lives.
// The current GetDBPath is never included, and an explicit VPNUI_DB_FOLDER means
// "this directory, do not look elsewhere": nil.
func LegacyDBPaths() []string {
	if os.Getenv("VPNUI_DB_FOLDER") != "" {
		return nil
	}
	bin := GetDBFolderPath()
	current := GetDBPath()
	dirs := []string{
		bin,
		filepath.Dir(bin),
		"/opt/vpn-ui",
		"/usr/local/vpn-ui",
		"/etc/vpn-ui",
		"/var/lib/vpn-ui",
		"/usr/local/x-ui",
		"/etc/x-ui",
		"/var/lib/x-ui",
		"/root",
	}
	seen := map[string]bool{current: true}
	var out []string
	for _, dir := range dirs {
		for _, name := range []string{"vpn-ui.db", "x-ui.db"} {
			p := filepath.Join(dir, name)
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// GetLogFolder returns the path to the log folder based on environment variables or platform defaults.
func GetLogFolder() string {
	logFolderPath := os.Getenv("VPNUI_LOG_FOLDER")
	if logFolderPath != "" {
		return logFolderPath
	}
	return "/var/log/vpn-ui"
}

// DB migration (moving/renaming a legacy database to GetDBPath) is handled
// cross-platform by database.InitDB via config.LegacyDBPaths — see migrateLegacyDB.
