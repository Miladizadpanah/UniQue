package service

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// Resetting an account's traffic must leave it the SAME account: same email,
// same subId on every membership, same quota, same expiry. The operator's
// expectation is "everything works again on the link I already gave the
// customer", and the way that breaks is a reset that either moves the account or
// rebuilds its settings from the traffic row.
//
// It also has to clear the two things that make the customer look dead: the
// counters the quota is measured against and the `enable` flag the depletion
// sweep flips.
func TestResetClientTrafficKeepsIdentityAndClearsUsage(t *testing.T) {
	newAccountsDB(t)
	const email = "keep@example.com"

	first := seedInboundWithClients(t, model.VMESS, 47211, []map[string]any{{
		"id": "11111111-1111-4111-8111-111111111111", "email": email,
		"enable": true, "subId": "sub-keep-me", "totalGB": float64(0),
	}})
	second := seedInboundWithClients(t, model.VLESS, 47212, []map[string]any{{
		"id": "22222222-2222-4222-8222-222222222222", "email": email,
		"enable": true, "subId": "sub-keep-me", "totalGB": float64(0),
	}})

	const mb = int64(1024 * 1024)
	expiry := time.Now().Add(72 * time.Hour).UnixMilli()
	if err := database.GetDB().Create(&xray.ClientTraffic{
		InboundId: first.Id, Email: email, Enable: false,
		Up: 10 * mb, Down: 20 * mb, AllTime: 99 * mb,
		Total: 30 * mb, ExpiryTime: expiry,
	}).Error; err != nil {
		t.Fatalf("seed client_traffics: %v", err)
	}

	if _, err := (&InboundService{}).ResetClientTraffic(first.Id, email); err != nil {
		t.Fatalf("ResetClientTraffic: %v", err)
	}

	row := accountTrafficOf(t, email)
	if row.Up != 0 || row.Down != 0 {
		t.Errorf("counters = (up %d, down %d); want zero — the quota is measured against these", row.Up, row.Down)
	}
	if !row.Enable {
		t.Error("the account is still disabled after a reset; the depletion sweep would keep it off")
	}
	if row.ExpiryTime != expiry {
		t.Errorf("expiry = %d; want %d untouched. A traffic reset is not a renewal", row.ExpiryTime, expiry)
	}
	if row.Total != 30*mb {
		t.Errorf("quota = %d; want %d untouched", row.Total, 30*mb)
	}
	if row.AllTime != 99*mb {
		t.Errorf("all_time = %d; want %d. A reset clears what counts against the quota, "+
			"never the lifetime record", row.AllTime, 99*mb)
	}

	// The subscription is built from settings.clients, so the subId must survive
	// on EVERY inbound serving the account or the customer's existing link stops
	// resolving to one of their endpoints.
	for _, id := range []int{first.Id, second.Id} {
		clients := readClients(t, id)
		if len(clients) != 1 {
			t.Fatalf("inbound %d holds %d clients after a reset; want 1", id, len(clients))
		}
		if got := subIdOf(t, clients[0]); got != "sub-keep-me" {
			t.Errorf("inbound %d subId = %q; want %q. The reset must not re-key the account", id, got, "sub-keep-me")
		}
		if enabled, _ := clients[0]["enable"].(bool); !enabled {
			t.Errorf("inbound %d stored the client as disabled after a reset", id)
		}
	}

	// A second reset, this time through the email entry point the Clients page
	// and the Telegram bot use, must behave identically.
	if err := (&InboundService{}).ResetClientTrafficByEmail(email); err != nil {
		t.Fatalf("ResetClientTrafficByEmail: %v", err)
	}
	if again := accountTrafficOf(t, email); again.Up != 0 || again.Down != 0 || !again.Enable {
		t.Errorf("the email entry point left the account at (up %d, down %d, enable %v)", again.Up, again.Down, again.Enable)
	}
}

// A node-only account has no local inbound AND no client_traffics row here: the
// hub only bookkeeps it while the node meters it. Reset used to resolve the
// account through an inbound it serves, so this exact account failed with
// "client not found" and the node half never ran. It must now succeed as a no-op
// locally, which is what lets the caller go on to reset the node copies.
func TestResetClientTrafficWithoutALocalRow(t *testing.T) {
	newAccountsDB(t)
	const email = "node-only@example.com"

	if err := database.GetDB().Create(&model.Account{Email: email, Enable: true}).Error; err != nil {
		t.Fatalf("seed account: %v", err)
	}

	needRestart, err := (&InboundService{}).ResetClientTrafficForEmail(email)
	if err != nil {
		t.Fatalf("ResetClientTrafficForEmail on a node-only account: %v", err)
	}
	if needRestart {
		t.Error("a node-only reset asked for a core restart; there is nothing local to restart")
	}
}

// The row is found by the folded identity, so the caller cannot miss it because
// the stored address differs in case or carries a trailing space. Missing the
// row also used to be indistinguishable from a failure.
func TestResetClientTrafficFoldsTheEmail(t *testing.T) {
	newAccountsDB(t)
	const stored = "Mixed@Example.com"

	if err := database.GetDB().Create(&xray.ClientTraffic{
		InboundId: 1, Email: stored, Enable: false, Up: 5, Down: 7,
	}).Error; err != nil {
		t.Fatalf("seed client_traffics: %v", err)
	}

	if _, err := (&InboundService{}).ResetClientTraffic(0, "  mixed@example.com "); err != nil {
		t.Fatalf("ResetClientTraffic: %v", err)
	}

	row := accountTrafficOf(t, stored)
	if row.Up != 0 || row.Down != 0 || !row.Enable {
		t.Errorf("row = (up %d, down %d, enable %v); want zeroed and enabled",
			row.Up, row.Down, row.Enable)
	}
}
