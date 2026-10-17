package service

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// ClientGroupService stores the operator's groups over their OWN client list.
//
// It is the account-centric twin of InboundGroupService, with two deliberate
// differences:
//
//  1. It is PER-USER. A group belongs to the user who made it (OwnerId); a caller
//     only ever reads, renames, deletes or re-members their own groups, and a
//     super admin sees all. That keeps one seller's client categories from
//     leaking to another, which the shared inbound groups are free to do because
//     their members are guarded to the inbounds a caller already owns.
//
//  2. It is FLAT. A group is a saved filter over the account list, not a nested
//     folder, so it needs no parent pointer and no cycle check: deleting one is
//     just "drop the row and its memberships", which is exactly the safe
//     guarantee the feature promises (a group is removed, its clients are not).
//
// A group changes nothing about an account and no protocol reads one, so every
// existing write path (add, edit, delete, Xray reload) stays unaware of it.
type ClientGroupService struct{}

// ClientGroupMemberRow is one client inside a group, enough for the panel to
// render a chip: its id, email and live enable state.
type ClientGroupMemberRow struct {
	Id     int    `json:"id"`
	Email  string `json:"email"`
	Enable bool   `json:"enable"`
}

// ClientGroupView is one group joined with its members, the only shape a caller
// wants: the group and the membership set live in two tables and the page needs
// both at once.
type ClientGroupView struct {
	Id      int                    `json:"id"`
	Name    string                 `json:"name"`
	OwnerId int                    `json:"ownerId"`
	Members []ClientGroupMemberRow `json:"members"`
	Count   int                    `json:"count"`
}

// ErrClientGroupName is returned when a group is saved without a name.
var ErrClientGroupName = errors.New("client group name is required")

// ErrClientGroupNotOwned is returned when a caller addresses a group that is not
// theirs. Surfaced, not silently reparented: a wrong-id write must not mutate a
// group the caller has no claim on.
var ErrClientGroupNotOwned = errors.New("client group not found or not owned by you")

// List returns the caller's groups, each with its members. A super admin gets
// every group; anyone else gets only the ones they own.
func (s *ClientGroupService) List(user *model.User) ([]ClientGroupView, error) {
	if user == nil {
		return []ClientGroupView{}, nil
	}
	db := database.GetDB()
	q := db.Model(&model.ClientGroup{})
	if !user.IsSuperAdmin {
		q = q.Where("owner_id = ?", user.Id)
	}
	var groups []model.ClientGroup
	if err := q.Order("id asc").Find(&groups).Error; err != nil {
		return nil, err
	}

	// One membership read for the whole result set, not one per group.
	groupIds := make([]int, 0, len(groups))
	for i := range groups {
		groupIds = append(groupIds, groups[i].Id)
	}
	byGroup := map[int][]int{}
	if len(groupIds) > 0 {
		var members []model.ClientGroupMember
		if err := db.Where("group_id IN ?", groupIds).Order("account_id asc").Find(&members).Error; err != nil {
			return nil, err
		}
		for _, m := range members {
			byGroup[m.GroupId] = append(byGroup[m.GroupId], m.AccountId)
		}
	}

	// Resolve ids to email + enable once. Enable is read from the account row: the
	// group panel is an organisational view, and pulling client_traffics just to
	// show a toggle here would be a second, subtly different, source of truth.
	acct := map[int]model.Account{}
	if ids := allMemberIds(byGroup); len(ids) > 0 {
		var accounts []model.Account
		if err := db.Where("id IN ?", ids).Find(&accounts).Error; err != nil {
			return nil, err
		}
		for i := range accounts {
			acct[accounts[i].Id] = accounts[i]
		}
	}

	out := make([]ClientGroupView, 0, len(groups))
	for i := range groups {
		g := &groups[i]
		ids := byGroup[g.Id]
		members := make([]ClientGroupMemberRow, 0, len(ids))
		for _, id := range ids {
			a, ok := acct[id]
			if !ok {
				// A membership whose account is gone: the account is deleted, so the
				// row is not a client the caller can show. Skipped, not a blank chip.
				continue
			}
			members = append(members, ClientGroupMemberRow{Id: a.Id, Email: a.Email, Enable: a.Enable})
		}
		out = append(out, ClientGroupView{
			Id: g.Id, Name: g.Name, OwnerId: g.OwnerId, Members: members, Count: len(members),
		})
	}
	return out, nil
}

// Create makes one group, owned by the caller.
func (s *ClientGroupService) Create(user *model.User, name string) (*model.ClientGroup, error) {
	if user == nil {
		return nil, errors.New("no user")
	}
	if err := validClientGroupName(name); err != nil {
		return nil, err
	}
	var group model.ClientGroup
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		group = model.ClientGroup{Name: name, OwnerId: user.Id}
		return tx.Create(&group).Error
	})
	if err != nil {
		return nil, err
	}
	return &group, nil
}

// Rename changes a group's name, after confirming the caller may edit it.
func (s *ClientGroupService) Rename(user *model.User, id int, name string) error {
	if err := validClientGroupName(name); err != nil {
		return err
	}
	return s.mutateOwned(user, id, func(tx *gorm.DB, g *model.ClientGroup) error {
		return tx.Model(g).Update("name", name).Error
	})
}

// Delete removes a group and its membership rows, never its accounts.
func (s *ClientGroupService) Delete(user *model.User, id int) error {
	return s.mutateOwned(user, id, func(tx *gorm.DB, g *model.ClientGroup) error {
		if err := tx.Where("group_id = ?", id).Delete(&model.ClientGroupMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ClientGroup{}, id).Error
	})
}

// SetMembers replaces a group's membership with exactly accountIds, after
// confirming the caller owns the group AND may see every account in the new
// set. A caller may not file another seller's clients under their own heading:
// the membership would be a record of data they cannot otherwise read.
func (s *ClientGroupService) SetMembers(user *model.User, id int, accountIds []int) error {
	var acc AccountService
	visible, err := acc.VisibleAccountIDs(user)
	if err != nil {
		return err
	}
	for _, id := range accountIds {
		if !visible[id] {
			return fmt.Errorf("account %d is not yours to group", id)
		}
	}
	return s.mutateOwned(user, id, func(tx *gorm.DB, g *model.ClientGroup) error {
		if err := tx.Where("group_id = ?", id).Delete(&model.ClientGroupMember{}).Error; err != nil {
			return err
		}
		for _, accId := range accountIds {
			if err := tx.Create(&model.ClientGroupMember{GroupId: id, AccountId: accId}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// mutateOwned loads the group, checks the caller may address it, and runs fn in a
// transaction under that guard. One place for the ownership rule keeps the three
// writes identical.
func (s *ClientGroupService) mutateOwned(user *model.User, id int, fn func(tx *gorm.DB, g *model.ClientGroup) error) error {
	if user == nil {
		return errors.New("no user")
	}
	db := database.GetDB()
	err := db.Transaction(func(tx *gorm.DB) error {
		var g model.ClientGroup
		if err := tx.First(&g, id).Error; err != nil {
			return ErrClientGroupNotOwned
		}
		if !user.IsSuperAdmin && g.OwnerId != user.Id {
			return ErrClientGroupNotOwned
		}
		return fn(tx, &g)
	})
	return err
}

func validClientGroupName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrClientGroupName
	}
	return nil
}

// allMemberIds is the union of every group's member ids, for the one accounts
// read in List.
func allMemberIds(byGroup map[int][]int) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, ids := range byGroup {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
