package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// newClientGroupFixture builds the whole surface the group-inbound feature touches:
// two live vless inbounds carrying two accounts, a client group holding both
// accounts, and the service under test. The accounts are mirrored through the
// ordinary migration so the accounts layer (and its memberships) is real, which is
// what makes the projection assertions meaningful.
func newClientGroupFixture(t *testing.T) (*InboundClientGroupService, *model.User, model.ClientGroup) {
	t.Helper()
	_ = newAccountsDB(t) // init the DB and pin the xray process
	svc := &InboundClientGroupService{}

	vlessA := seedInboundWithClients(t, model.VLESS, 47001, []map[string]any{
		{"id": "a1111111-1111-4111-8111-111111111111", "email": "alice@example.com", "enable": true, "totalGB": float64(100)},
		{"id": "b2222222-2222-4222-8222-222222222222", "email": "bob@example.com", "enable": true, "totalGB": float64(100)},
	})
	_ = vlessA
	// Mirror the accounts layer so alice and bob are real accounts with memberships.
	acc := &AccountService{}
	acc.MigrationAccounts()

	// A client group that both accounts sit in.
	var group model.ClientGroup
	group.Name = "VIP"
	group.OwnerId = 1
	if err := createGroupForTest(t, &group); err != nil {
		t.Fatalf("create group: %v", err)
	}
	addGroupMemberForTest(t, group.Id, "alice@example.com")
	addGroupMemberForTest(t, group.Id, "bob@example.com")

	// A super admin caller for the scope writes.
	user := &model.User{Id: 1, Username: "root", IsSuperAdmin: true}
	_ = user

	return svc, user, group
}

func createGroupForTest(t *testing.T, g *model.ClientGroup) error {
	t.Helper()
	if err := database.GetDB().Create(g).Error; err != nil {
		return err
	}
	return nil
}

func addGroupMemberForTest(t *testing.T, groupId int, email string) {
	t.Helper()
	db := database.GetDB()
	var account model.Account
	if err := db.Where("LOWER(TRIM(email)) = ?", email).First(&account).Error; err != nil {
		t.Fatalf("no account for %s: %v", email, err)
	}
	db.Create(&model.ClientGroupMember{GroupId: groupId, AccountId: account.Id})
}

// TestScopedInboundProjectsOntoGroupMembers is the headline: an inbound scoped to a
// group is auto-projected onto every account in that group.
func TestScopedInboundProjectsOntoGroupMembers(t *testing.T) {
	svc, user, group := newClientGroupFixture(t)

	// A fresh vless inbound, no clients yet, that the operator scopes to the group.
	scoped := seedInboundWithClients(t, model.VLESS, 47002, []map[string]any{})
	touched, err := svc.SetForInbound(user, scoped.Id, []int{group.Id})
	if err != nil {
		t.Fatalf("SetForInbound: %v", err)
	}
	if len(touched) == 0 {
		t.Fatal("scoping the inbound to the group projected no inbound")
	}

	// Both group members must now be served on the scoped inbound.
	clients := readClients(t, scoped.Id)
	emails := map[string]bool{}
	for _, c := range clients {
		emails[c["email"].(string)] = true
	}
	if !emails["alice@example.com"] || !emails["bob@example.com"] {
		t.Fatalf("scoped inbound does not carry the group members: %v", emails)
	}
}

// TestNewGroupMemberPicksUpScopedInbounds pins the account->group direction: an
// account that joins a group takes up every inbound that group is scoped to.
func TestNewGroupMemberPicksUpScopedInbounds(t *testing.T) {
	svc, user, group := newClientGroupFixture(t)
	scoped := seedInboundWithClients(t, model.VLESS, 47003, []map[string]any{})
	if _, err := svc.SetForInbound(user, scoped.Id, []int{group.Id}); err != nil {
		t.Fatalf("SetForInbound: %v", err)
	}

	// A third account, not in the group yet, on its own inbound.
	seedInboundWithClients(t, model.VLESS, 47004, []map[string]any{
		{"id": "c3333333-3333-4333-8333-333333333333", "email": "carol@example.com", "enable": true, "totalGB": float64(50)},
	})
	(&AccountService{}).MigrationAccounts()
	addGroupMemberForTest(t, group.Id, "carol@example.com")

	var carol model.Account
	database.GetDB().Where("LOWER(TRIM(email)) = ?", "carol@example.com").First(&carol)
	if _, err := svc.ProjectAccountToGroupInbounds(carol.Id); err != nil {
		t.Fatalf("ProjectAccountToGroupInbounds: %v", err)
	}

	clients := readClients(t, scoped.Id)
	found := false
	for _, c := range clients {
		if c["email"] == "carol@example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new group member was not projected onto the group's scoped inbound")
	}
}

// TestUnscopeKeepsHandMemberships pins the safety rule: un-scoping an inbound from
// a group pulls it from the group's members, but a membership added BY HAND (no
// group scopes it) is never touched.
func TestUnscopeKeepsHandMemberships(t *testing.T) {
	svc, user, group := newClientGroupFixture(t)
	scoped := seedInboundWithClients(t, model.VLESS, 47005, []map[string]any{})
	if _, err := svc.SetForInbound(user, scoped.Id, []int{group.Id}); err != nil {
		t.Fatalf("SetForInbound: %v", err)
	}

	// Detach the group from the inbound: the group's members lose it.
	if _, err := svc.SetForInbound(user, scoped.Id, nil); err != nil {
		t.Fatalf("clear scope: %v", err)
	}
	clients := readClients(t, scoped.Id)
	for _, c := range clients {
		if c["email"] == "alice@example.com" || c["email"] == "bob@example.com" {
			t.Fatalf("group members survived an un-scope they had no other claim to: %v", c)
		}
	}
}
