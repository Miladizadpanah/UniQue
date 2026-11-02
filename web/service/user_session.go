package service

// Session management: the panel's active-login surface. The session store is a
// cookie (gin-contrib/sessions), which it cannot enumerate server-side, so
// the active-sessions surface is a SHADOW LOG: the login path records each
// session here, and the revoke path marks the row revoked. The next request
// from a revoked session loads its user row, sees the flag, and logs out.
//
// One row per login: who (the user id), where (IP + user agent), when
// (createdAt), and whether it has been revoked (revokedAt, 0 = never). The
// log is bounded two ways: the retention pruner drops rows older than the
// max session age (a session that has expired is a dead one, and its row
// costs nothing to lose), and the per-user cap keeps a chatty admin from
// filling the table with one row per login.

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

// UserSessionRetention is the cap on how many session rows ONE user may hold
// at once. 50 covers an admin who logs in from 50 browsers and never logs
// out; beyond that the oldest rows are pruned on the next login. A user who
// genuinely logs in from 51+ devices at once is not the operator this panel
// is built for.
const UserSessionRetention = 50

// UserSessionMaxAge is how long an UNREVOKED session row is kept before the
// retention pruner drops it. It matches the cookie's own max age (the
// session cookie lives 6h, so a row older than that is a dead session and
// its log line costs nothing to lose). A revoked row is kept a little
// longer than its max age so the operator's "active sessions" view still
// shows the revocation for a while after it happened.
const UserSessionMaxAge = 24 * time.Hour

// NewSessionFingerprint mints a random 32-hex-char session identifier. The
// login path stores it in the session cookie's session store under its own
// key, and the revoke path matches it against the row to decide which
// session the operator is revoking. A collision is not a concern (32 hex
// chars = 128 bits of entropy), and a missing fingerprint on an old session
// (one logged in before this feature shipped) simply means that session
// cannot be individually revoked — the operator's "log out everywhere"
// action is the fallback for it.
func NewSessionFingerprint() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// The CSPRNG failing on a Linux box with /dev/urandom is a
		// kernel-level problem, not a panel one; an empty fingerprint
		// means "this session is unaddressable" rather than "the panel
		// crashed on login", which is the honest failure shape.
		logger.Warning("session fingerprint generation failed:", err)
		return ""
	}
	return hex.EncodeToString(buf)
}

// RecordSession stores one login's row against the user's session log. A
// blank fingerprint (a login that predates the feature, or a CSPRNG
// failure) is recorded with the empty fingerprint: the row still counts
// toward the user's active total, and the per-user cap still prunes it.
// A nil DB (a unit-test harness that never opened one) is a no-op, not an
// error: the shadow log is a dashboard nicety, and a failing DB write here
// would not help the caller anyway.
func RecordSession(userId int, fingerprint, ip, userAgent string) {
	if database.GetDB() == nil {
		return
	}
	db := database.GetDB()
	row := &model.UserSession{
		UserId:      userId,
		Fingerprint: fingerprint,
		Ip:          ip,
		UserAgent:   userAgent,
		CreatedAt:   time.Now().UnixMilli(),
	}
	if err := db.Create(row).Error; err != nil {
		logger.Warning("recording user session:", err)
		return
	}
	// The per-user cap: prune the oldest rows beyond the retention window,
	// so a chatty admin (or an operator testing from 50+ browsers) does
	// not fill the table. One cheap DELETE per login is the cheapest place
	// to keep the log bounded.
	pruneUserSessionRows(userId, UserSessionRetention)
}

// ListSessions returns the user's active (unrevoked) session rows, oldest
// first, for the "active sessions" tile on the Admins page. An empty list
// (a user who has not logged in since the feature shipped, or whose rows
// have all been pruned) is a valid answer, not an error.
func ListSessions(userId int) ([]model.UserSession, error) {
	if database.GetDB() == nil {
		return nil, nil
	}
	var out []model.UserSession
	err := database.GetDB().
		Where("user_id = ? AND revoked_at = ?", userId, 0).
		Order("created_at asc").
		Find(&out).Error
	return out, err
}

// RevokeSession marks one session row (by its fingerprint) revoked. The
// next request from that browser loads its user row through the session,
// and the login middleware checks whether the session has been revoked; a
// revoked session is logged out, and the operator's "active sessions" view
// re-renders from the same table. A revoked row is kept (with its
// revoked_at stamp) so the operator can see the revocation in the log,
// until the retention pruner drops it.
func RevokeSession(fingerprint string) error {
	if database.GetDB() == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	return database.GetDB().
		Model(&model.UserSession{}).
		Where("fingerprint = ? AND revoked_at = ?", fingerprint, 0).
		Update("revoked_at", now).Error
}

// RevokeAllSessionsForUser marks every active (unrevoked) session row for
// one user revoked, so the next request from ANY of that user's browsers
// logs them out. The operator's "force logout" action on the Admins page
// calls this. A per-fingerprint revoke (RevokeSession) is the targeted
// version: this one is the blanket version, used when the operator does
// not know which of the user's sessions to keep.
func RevokeAllSessionsForUser(userId int) error {
	if database.GetDB() == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	return database.GetDB().
		Model(&model.UserSession{}).
		Where("user_id = ? AND revoked_at = ?", userId, 0).
		Update("revoked_at", now).Error
}

// SessionIsRevoked answers "is this browser's session revoked": the login
// middleware calls it on every request, and a revoked session is logged
// out on the next one. A blank fingerprint (a session that predates the
// feature, or a CSPRNG failure) is not revoked: the operator cannot
// target it individually, and the blanket "force logout" (which marks every
// row for the user revoked) is the only path that catches it. A nil DB
// (a unit-test harness that never opened one) answers "not revoked", which
// is the safe default: a panel that cannot read its session log keeps its
// operator logged in rather than locking them out.
func SessionIsRevoked(fingerprint string) bool {
	if fingerprint == "" || database.GetDB() == nil {
		return false
	}
	var count int64
	database.GetDB().
		Model(&model.UserSession{}).
		Where("fingerprint = ? AND revoked_at > 0", fingerprint).
		Count(&count)
	return count > 0
}

// PruneUserSessionRows drops the user's oldest session rows beyond the
// per-user cap, keeping at most `keep` rows (the newest ones, revoked or
// not). Called on every login (RecordSession) so the table stays bounded
// without a separate job: the login path already touches the user's rows,
// and one cheap DELETE per login is the cheapest place to prune.
func pruneUserSessionRows(userId int, keep int) {
	db := database.GetDB()
	if db == nil {
		return
	}
	var ids []int
	db.Model(&model.UserSession{}).
		Where("user_id = ?", userId).
		Order("created_at desc, id desc").
		Limit(keep).
		Pluck("id", &ids)
	if len(ids) == 0 {
		return
	}
	db.Where("user_id = ? AND id NOT IN ?", userId, ids).
		Delete(&model.UserSession{})
}

// UserHasRevokedSessionForUser answers "was the CURRENT session revoked by
// the blanket 'force logout' action": the per-user revocation check the
// session loader runs alongside the per-fingerprint check. It is keyed on
// the session's OWN fingerprint: a session that predates the feature has
// no fingerprint and so is not individually addressable, but the blanket
// revoke marks EVERY active row for the user, and the next request from
// that browser loads its row by user id, so the per-user check is what
// catches it.
//
// A nil DB (a unit-test harness that never opened one) answers "no", which
// is the safe default: a panel that cannot read its session log keeps its
// operator logged in rather than locking them out.
func UserHasRevokedSessionForUser(userId int, fingerprint string) bool {
	if database.GetDB() == nil {
		return false
	}
	var count int64
	// The session is revoked when EITHER its own row is marked revoked
	// (the targeted path) OR, when it has no fingerprint of its own, when
	// ANY of the user's rows is marked revoked (the blanket path). The
	// second disjunct is what catches a pre-feature session on a "force
	// logout" action.
	if fingerprint != "" {
		database.GetDB().
			Model(&model.UserSession{}).
			Where("user_id = ? AND fingerprint = ? AND revoked_at > 0", userId, fingerprint).
			Count(&count)
	} else {
		// No fingerprint: the session predates the feature (or the CSPRNG
		// failed at login). The blanket "force logout" marked every row for
		// the user, so ANY revoked row means this session is logged out.
		database.GetDB().
			Model(&model.UserSession{}).
			Where("user_id = ? AND revoked_at > 0", userId).
			Count(&count)
	}
	return count > 0
}

// PruneStaleSessions drops session rows older than the max session age,
// across every user. The 60s cron already walks the panel's own gauges
// (RecordLocalMetrics), and one cheap DELETE on the same tick keeps the
// session log bounded without a dedicated job. A revoked row is kept
// longer than a live one (the operator's "active sessions" view still
// shows the revocation for a while after it happened), so the prune only
// drops rows that are BOTH revoked AND older than the max age.
func PruneStaleSessions() int64 {
	db := database.GetDB()
	if db == nil {
		return 0
	}
	cutoff := time.Now().Add(-UserSessionMaxAge).UnixMilli()
	res := db.Where("revoked_at > 0 AND revoked_at < ?", cutoff).
		Delete(&model.UserSession{})
	if res.Error == nil {
		// Also drop unrevoked rows that are older than the max session
		// age: a session cookie that has expired (6h) leaves a row that
		// will never be read again, and its log line costs nothing to
		// lose once the cookie itself is gone.
		res2 := db.Where("revoked_at = 0 AND created_at < ?", cutoff).
			Delete(&model.UserSession{})
		if res2.Error == nil {
			return res.RowsAffected + res2.RowsAffected
		}
	}
	return res.RowsAffected
}
