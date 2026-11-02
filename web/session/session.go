// Package session provides session management utilities for the vpn-ui web panel.
// It handles user authentication state, login sessions, and session storage using Gin sessions.
package session

import (
	"net/http"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/web/service"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const (
	loginUserKey = "LOGIN_USER"
	// loginUserCtxKey caches the loaded user for the life of one request, so the
	// several handlers that ask for it don't each re-query.
	loginUserCtxKey = "LOGIN_USER_ROW"
	// loginFingerprintKey is the session's identifying token: a random 32-hex
	// value minted at login and stored in the cookie's session store under its
	// own key, so a session the operator revokes can be identified by the
	// cookie alone. A missing key (a session that predates the feature) means
	// "unaddressable": the operator's blanket "log out everywhere" is the
	// only path that catches it.
	loginFingerprintKey = "SESSION_FINGERPRINT"
)

// The session cookie holds ONLY the user's id, never the user row.
//
// The store is signed but not encrypted (cookie.NewStore -> securecookie with a
// nil blockKey), so its gob body is readable in the browser with base64 -d. It
// used to carry the whole model.User, which put the admin's bcrypt hash in every
// browser and would have put their TOTP secret there too.
//
// Loading the row per request also means permission changes and account disables
// take effect immediately instead of lingering until the cookie's 6h expiry, and
// it sidesteps securecookie's 4096-byte cap.
//
// Upgrade note: a pre-upgrade cookie holds a gob'd model.User, so the int type
// assertion below fails and the session soft-logs-out. That is the intended
// migration: one forced re-login, rather than a stale snapshot decoding with
// zero permissions into a panel where every action silently 403s.

// SetLoginUser stores the authenticated user's id in the session, and mints a
// session fingerprint for the active-sessions surface. The fingerprint is the
// operator's handle for revoking THIS session specifically (the Admins page's
// "revoke this session" button); a blank fingerprint (a CSPRNG failure) means
// the session is unaddressable and the blanket "log out everywhere" is the
// fallback.
func SetLoginUser(c *gin.Context, user *model.User) {
	if user == nil {
		return
	}
	s := sessions.Default(c)
	s.Set(loginUserKey, user.Id)
	// Mint the fingerprint only on a fresh session (one that has no key yet):
	// a re-login from the same browser after a soft-logout must not mint a
	// second fingerprint and leave the old row orphaned. The login path calls
	// this after a successful CheckUser, so the key is absent on the first
	// call and present on any same-session retry.
	if s.Get(loginFingerprintKey) == nil {
		s.Set(loginFingerprintKey, service.NewSessionFingerprint())
	}
	c.Set(loginUserCtxKey, user)
}

// sessionFingerprint returns the session's identifying token, or "" when the
// session predates the feature or the fingerprint was not minted. The revoke
// path matches this against the row to decide which session the operator is
// revoking; a blank answer means "this session cannot be individually
// revoked".
func sessionFingerprint(c *gin.Context) string {
	s := sessions.Default(c)
	fp, ok := s.Get(loginFingerprintKey).(string)
	if !ok {
		return ""
	}
	return fp
}

// RecordActiveSession is the login path's half of the shadow log: it stores
// one row against the user's session log so the Admins page's "active
// sessions" tile can show which browsers are logged in. The fingerprint is
// the session's own identifying token (the one SetLoginUser minted); the IP
// and user agent are the caller's, so the operator sees WHERE the session
// came from without opening the access log.
//
// A nil DB (a unit-test harness that never opened one) is a no-op: the
// shadow log is a dashboard nicety, and a failing DB write here would not
// help the caller anyway.
func RecordActiveSession(c *gin.Context, user *model.User) {
	if user == nil {
		return
	}
	service.RecordSession(user.Id, sessionFingerprint(c), c.ClientIP(), c.Request.UserAgent())
}

// loginUserId returns the session's user id, or 0 when absent or unreadable.
func loginUserId(c *gin.Context) int {
	s := sessions.Default(c)
	obj := s.Get(loginUserKey)
	if obj == nil {
		return 0
	}
	id, ok := obj.(int)
	if !ok || id <= 0 {
		// Garbage, or a pre-upgrade cookie holding a gob'd model.User.
		s.Delete(loginUserKey)
		return 0
	}
	return id
}

// GetLoginUser returns the CURRENT row for the logged-in admin, read fresh from
// the database rather than trusted from the cookie. Returns nil when logged out,
// when the account has been deleted, or when it has been disabled. A session
// the operator has REVOKED also answers nil: the next request from that
// browser loads its user row, sees the session is revoked, and logs out.
func GetLoginUser(c *gin.Context) *model.User {
	if cached, exists := c.Get(loginUserCtxKey); exists {
		user, _ := cached.(*model.User)
		return user
	}
	id := loginUserId(c)
	if id == 0 {
		return nil
	}
	// A revoked session is logged out on this request: the operator's
	// "revoke this session" action marked the row, and the cookie still
	// carries the user's id, so without this check the session would keep
	// working until its 6h expiry. The check is the shadow log's read
	// half, and it covers BOTH revocation paths:
	//
	//   - the per-fingerprint check (SessionIsRevoked): the operator's
	//     "revoke THIS session" button targets the session's own token;
	//   - the per-user check (UserHasRevokedSessionForUser): the operator's
	//     "force log out everywhere" button marks every row for the user
	//     revoked, which is the only path that catches a session that
	//     predates the feature (no fingerprint of its own).
	if service.SessionIsRevoked(sessionFingerprint(c)) || service.UserHasRevokedSessionForUser(id, sessionFingerprint(c)) {
		ClearSession(c)
		return nil
	}
	user := loadLoginUser(c)
	c.Set(loginUserCtxKey, user)
	return user
}

func loadLoginUser(c *gin.Context) *model.User {
	id := loginUserId(c)
	if id == 0 {
		return nil
	}
	db := database.GetDB()
	if db == nil {
		return nil
	}
	user := &model.User{}
	if err := db.Model(model.User{}).Where("id = ?", id).First(user).Error; err != nil {
		return nil // deleted mid-session, or the DB is unavailable
	}
	if !user.Enable {
		return nil // disabled admins are logged out on their next request
	}
	return user
}

// IsLogin checks if a user is currently authenticated in the session.
// Returns true if a valid user session exists, false otherwise.
func IsLogin(c *gin.Context) bool {
	return GetLoginUser(c) != nil
}

// ClearSession removes all session data and invalidates the session.
// This effectively logs out the user and clears any stored session information.
func ClearSession(c *gin.Context) {
	s := sessions.Default(c)
	s.Clear()
	cookiePath := c.GetString("base_path")
	if cookiePath == "" {
		cookiePath = "/"
	}
	s.Options(sessions.Options{
		Path:     cookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
