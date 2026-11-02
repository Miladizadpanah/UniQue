package service

// Scoped API tokens: the operator's granular node-API credentials. The shared
// nodeApiToken (the settings-table value the status endpoint has always read)
// remains the "full access" credential; an ApiToken row is the granular
// version — a random 64-hex secret, a scope bitmask, and an optional expiry.
//
// The two paths coexist, and a panel that has never minted a scoped token
// behaves exactly as before: the shared token is the only answer, and it is
// still the default. The gate in web/controller/node_api.go accepts EITHER
// credential — the shared node token (full access, the legacy path) or one
// of the panel's own scoped tokens (the scope restricts the routes it may
// open) — so an operator who hands a hub a read-only token does not lose
// the legacy full-access path until they explicitly stop using it.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
)

// ApiTokenService owns the api_tokens table: mint, list, rotate, delete.
// Zero-value usable, like the other panel services.
type ApiTokenService struct{}

// GenerateTokenSecret mints a 64-hex-char secret, the same shape the shared
// node token and the tunnel engine's tokens use: random, one-time, no
// structure to guess.
func (s *ApiTokenService) GenerateTokenSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", common.NewError("generating token secret: ", err.Error())
	}
	return hex.EncodeToString(buf), nil
}

// CreateToken mints one scoped token: a random secret under the operator's
// chosen scope, with an optional expiry (UnixMilli; 0 = no expiry). The
// secret is minted HERE, not taken from the caller: the operator names the
// token and its scope, and the panel decides the credential, the same way
// the tunnel engine mints its own tokens. The minted secret is returned ONCE,
// in the create's answer; the table stores it in the clear (like the other
// panel credentials) and the UI shows it for copy, so a lost secret is a
// re-mint, not a recovery.
func (s *ApiTokenService) CreateToken(name string, scope int64, expiresAt int64, remark string) (*model.ApiToken, error) {
	if name == "" {
		return nil, common.NewError("token name is required")
	}
	secret, err := s.GenerateTokenSecret()
	if err != nil {
		return nil, err
	}
	// Only the scope bits that exist are kept: a caller posting a bit the
	// panel does not know about (a newer build's scope, a hand-edited
	// request) is clamped to the known set rather than stored with a
	// scope the gate will never recognise. 0 is a valid scope (read-only
	// on the shared token's own read routes), so the clamp is "AND with
	// the known mask", not "refuse 0".
	var knownMask int64
	for _, d := range model.AllApiTokenScopes {
		knownMask |= d.Bit
	}
	scope &= knownMask
	row := &model.ApiToken{
		Name:      name,
		Secret:    secret,
		Scope:     scope,
		ExpiresAt: expiresAt,
		Remark:    remark,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := database.GetDB().Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// List returns every scoped token the operator has minted, newest first.
// The secrets are included: the operator who minted them can copy them
// back to a hub, and the table stores them in the clear, so the list is
// the answer the Nodes page's token tile needs. An empty list is the
// "no scoped tokens yet" case, not an error.
func (s *ApiTokenService) List() ([]model.ApiToken, error) {
	var out []model.ApiToken
	err := database.GetDB().Order("id desc").Find(&out).Error
	return out, err
}

// Rotate re-mints one token's secret: the operator's "rotate this hub's
// credential" action. The old secret stops working the moment the row is
// saved, so a hub still holding it is refused on its next request until
// the operator hands it the new one — the same rotation semantics as the
// shared node token (which is a single settings-table value, so its
// rotation is a one-field write).
func (s *ApiTokenService) Rotate(id int) (*model.ApiToken, error) {
	var row model.ApiToken
	if err := database.GetDB().First(&row, id).Error; err != nil {
		return nil, err
	}
	secret, err := s.GenerateTokenSecret()
	if err != nil {
		return nil, err
	}
	row.Secret = secret
	if err := database.GetDB().Save(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Delete removes one token by id: the operator's "revoke this hub's
// credential" action. A hub still holding the deleted secret is refused
// on its next request, and the shared node token (if set) is the
// fallback the operator has always had.
func (s *ApiTokenService) Delete(id int) error {
	return database.GetDB().Delete(&model.ApiToken{}, "id = ?", id).Error
}

// ResolveScope answers "which routes may this credential open": the
// node API gate calls it with the X-Node-Token header's value, and the
// answer is the scope bitmask the request's route must fall inside.
//
// The two paths, in order:
//
//  1. The shared node token: a constant-time compare against the
//     settings-table value. A match is FULL access — the legacy path,
//     and the one a panel that has never minted a scoped token still
//     relies on. The scope returned is "everything" (the all-bits mask),
//     so the gate's per-route scope check passes every route the shared
//     token has always opened.
//
//  2. A scoped token: the value is looked up in the api_tokens table. A
//     hit that is not expired answers that row's own scope, and the gate
//     then checks the route's required scope against it. A miss, an
//     expired row, or an empty header answers "no access": the shared
//     token is the only full-access path, and a scoped token that does
//     not carry the route's scope is refused, not escalated.
//
// A blank header and a blank shared token answer "no access", the same
// safe-closed default the shared path has always had: a panel is not
// remotely manageable until its operator opts in.
//
// The return's ok flag is false when the credential is not one of this
// panel's; the scope is the all-bits mask when the shared token matched
// and the row's own scope otherwise. The caller (the gate) treats a
// blank scope as "no scoped routes" and the all-bits mask as "full
// access", so the two answers are distinct.
func (s *ApiTokenService) ResolveScope(headerValue string) (scope int64, ok bool) {
	db := database.GetDB()
	if db == nil || headerValue == "" {
		return 0, false
	}
	// Path 1: the shared node token, the legacy full-access credential.
	// A constant-time compare, the same one the status endpoint has
	// always used, so a timing side channel is not introduced by the
	// scoped path.
	var settingService SettingService
	shared := settingService.GetNodeApiToken()
	if shared != "" {
		if subtle.ConstantTimeCompare([]byte(shared), []byte(headerValue)) == 1 {
			// Full access: every scope bit. The all-bits mask is the
			// answer that makes the gate's per-route check pass every
			// route the shared token has always opened.
			var all int64
			for _, d := range model.AllApiTokenScopes {
				all |= d.Bit
			}
			return all, true
		}
	}
	// Path 2: a scoped token. Look the header's value up in the table.
	var row model.ApiToken
	if err := db.Where("secret = ?", headerValue).First(&row).Error; err != nil {
		return 0, false // miss: not one of this panel's scoped tokens
	}
	// An expired token is a miss, not a scope: the operator set a
	// deadline, and a past deadline is the "no access" answer, exactly
	// the way a deleted token's secret stops working on the next
	// request.
	if row.ExpiresAt > 0 && time.Now().UnixMilli() > row.ExpiresAt {
		return 0, false
	}
	return row.Scope, true
}

// The shared token's full-access mask carries every bit, so a shared-token
// request passes every route the way it always has.
const (
	// RouteScopeRead is the scope one node API read route requires:
	// /inbounds, /clients, /pairing/status, /mtls/ca.
	RouteScopeRead = model.ScopeNodeRead
	// RouteScopeWrite is the scope one node API write route requires:
	// /inbounds (create), /clients, /delClient, /purge, /mtls/sign,
	// /pairing/request.
	RouteScopeWrite = model.ScopeNodeWrite
	// RouteScopeTelemetry is the scope the node API's telemetry route
	// requires: /heartbeat, the push side of the hub<->node 30s beat.
	RouteScopeTelemetry = model.ScopeHeartbeat
)
