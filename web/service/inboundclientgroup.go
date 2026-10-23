package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// InboundClientGroupService wires an inbound to the client groups it should serve,
// and keeps the account-membership layer in step with both directions of that link.
//
// The link is model.InboundClientGroup (inbound_id, group_id). It is purely an
// auto-projection rule:
//
//   - inbound -> group: assigning an inbound to a group projects it onto every
//     account already in the group, and it stays projected onto accounts that join
//     the group later.
//   - account -> group: when an account's group set grows, the account picks up every
//     inbound those groups carry; when it shrinks, it loses an inbound only if no
//     remaining group still claims it.
//
// An inbound with no link at all is "all users" and projects nothing by itself: its
// membership comes only from the explicit per-client writes, byte-for-byte as before.
// That is the safety property the whole feature relies on - a scoped inbound is
// additive, and nothing is ever disconnected on the last claim.
type InboundClientGroupService struct{}

// ErrNoUser is returned for a nil caller: there is no owner to attach the link to.
var ErrNoUser = errors.New("no user")

// ErrInboundNotOwned is returned when the caller addresses an inbound they cannot
// edit. Surfaced, not silently re-pointed: a wrong-id write must not scope somebody
// else's inbound.
var ErrInboundNotOwned = errors.New("inbound not found or not yours to edit")

// ErrGroupNotOwned is returned when the caller names a client group that is not
// theirs. A group is per-user (see ClientGroup), so scoping an inbound to another
// seller's group would leak their client categories.
var ErrGroupNotOwned = errors.New("client group not found or not owned by you")

// GroupsForInbound lists the client groups an inbound is currently scoped to.
func (s *InboundClientGroupService) GroupsForInbound(inboundId int) []int {
	var links []model.InboundClientGroup
	if err := database.GetDB().Where("inbound_id = ?", inboundId).
		Order("group_id asc").Find(&links).Error; err != nil {
		return nil
	}
	out := make([]int, 0, len(links))
	for _, l := range links {
		out = append(out, l.GroupId)
	}
	return out
}

// InboundsForGroup lists the inbounds a client group is scoped to.
func (s *InboundClientGroupService) InboundsForGroup(groupId int) []int {
	var links []model.InboundClientGroup
	if err := database.GetDB().Where("group_id = ?", groupId).
		Order("inbound_id asc").Find(&links).Error; err != nil {
		return nil
	}
	out := make([]int, 0, len(links))
	for _, l := range links {
		out = append(out, l.InboundId)
	}
	return out
}

// InboundsForAccount is the union of every inbound the account's groups carry.
// An account in no group gets nothing back, which is exactly "behaves as before".
func (s *InboundClientGroupService) InboundsForAccount(accountId int) []int {
	return s.inboundsForAccountDB(database.GetDB(), accountId)
}

// MemberAccountIDs lists the account ids currently in a client group. The
// membership-diff in the panel's group-members route uses it to know which accounts
// a change touched, so the inbound projection can be scoped to exactly those.
func (s *InboundClientGroupService) MemberAccountIDs(groupId int) []int {
	var members []model.ClientGroupMember
	if err := database.GetDB().Where("group_id = ?", groupId).Find(&members).Error; err != nil {
		return nil
	}
	out := make([]int, 0, len(members))
	for _, m := range members {
		out = append(out, m.AccountId)
	}
	return out
}

func (s *InboundClientGroupService) inboundsForAccountDB(db *gorm.DB, accountId int) []int {
	var memberGroups []model.ClientGroupMember
	if err := db.Where("account_id = ?", accountId).Find(&memberGroups).Error; err != nil {
		return nil
	}
	seen := map[int]bool{}
	out := []int{}
	for _, mg := range memberGroups {
		links, _ := s.linkRowsForGroup(db, mg.GroupId)
		for _, l := range links {
			if !seen[l.InboundId] {
				seen[l.InboundId] = true
				out = append(out, l.InboundId)
			}
		}
	}
	return out
}

func (s *InboundClientGroupService) linkRowsForGroup(db *gorm.DB, groupId int) ([]model.InboundClientGroup, error) {
	var links []model.InboundClientGroup
	if err := db.Where("group_id = ?", groupId).Find(&links).Error; err != nil {
		return nil, err
	}
	return links, nil
}

// SetForInbound replaces an inbound's group scope with exactly groupIds, then keeps
// the membership layer in step. It returns the inbound ids whose settings actually
// changed, for the caller to reconcile (an Xray restart when a native protocol is
// involved).
//
// Rules:
//   - newly attached groups: every account in the group gets the inbound.
//   - detached groups: an account in the group loses the inbound ONLY when no other
//     group still claims it for them. An explicit membership the operator added by
//     hand is indistinguishable from a projected one in the flat table, so the
//     "no other group claim" test is the only safe thing to key on: it never pulls
//     the inbound away from anyone who still has a live claim.
//
// The caller owns the inbound (the controller's owns/requirePerm guards do that) and
// must own every group it names; this method re-checks group ownership because the
// link, once written, reaches every member of those groups.
func (s *InboundClientGroupService) SetForInbound(user *model.User, inboundId int, groupIds []int) ([]int, error) {
	if user == nil {
		return nil, ErrNoUser
	}
	// Normalise: de-dupe, drop zero ids, stable order. A caller may tick the same
	// group twice in the picker.
	want := map[int]bool{}
	clean := make([]int, 0, len(groupIds))
	for _, id := range groupIds {
		if id <= 0 || want[id] {
			continue
		}
		want[id] = true
		clean = append(clean, id)
	}

	// Validate the inbound exists and is editable before anything is written.
	var inbound model.Inbound
	if err := database.GetDB().First(&inbound, inboundId).Error; err != nil {
		return nil, ErrInboundNotOwned
	}

	// Validate group ownership up front so a half-applied scope is impossible.
	for _, id := range clean {
		var g model.ClientGroup
		if err := database.GetDB().First(&g, id).Error; err != nil {
			return nil, ErrGroupNotOwned
		}
		if !user.IsSuperAdmin && g.OwnerId != user.Id {
			return nil, ErrGroupNotOwned
		}
	}

	touched := []int{}
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		var existing []model.InboundClientGroup
		if err := tx.Where("inbound_id = ?", inboundId).Find(&existing).Error; err != nil {
			return err
		}
		have := map[int]bool{}
		for _, l := range existing {
			have[l.GroupId] = true
		}

		// Attachments: groups present in want but not have.
		for _, gid := range clean {
			if !have[gid] {
				if err := tx.Create(&model.InboundClientGroup{InboundId: inboundId, GroupId: gid}).Error; err != nil {
					return err
				}
				ids, err := s.projectInboundToGroupTx(tx, inboundId, gid)
				if err != nil {
					return err
				}
				touched = appendInts(touched, ids)
			}
		}
		// Detachments: groups in have but not want.
		for gid := range have {
			if want[gid] {
				continue
			}
			if err := tx.Where("inbound_id = ? AND group_id = ?", inboundId, gid).
				Delete(&model.InboundClientGroup{}).Error; err != nil {
				return err
			}
			ids, err := s.dropInboundFromGroupTx(tx, inboundId, gid)
			if err != nil {
				return err
			}
			touched = appendInts(touched, ids)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dedupeInts(touched), nil
}

// projectInboundToGroupTx makes every account in the group a member of the inbound.
// Idempotent: an account already there is a no-op. Returns the inbound ids the
// projections touched.
func (s *InboundClientGroupService) projectInboundToGroupTx(tx *gorm.DB, inboundId, groupId int) ([]int, error) {
	var members []model.ClientGroupMember
	if err := tx.Where("group_id = ?", groupId).Find(&members).Error; err != nil {
		return nil, err
	}
	touched := []int{}
	for _, m := range members {
		ids, err := s.ensureMembershipTx(tx, m.AccountId, inboundId)
		if err != nil {
			return nil, err
		}
		touched = appendInts(touched, ids)
	}
	return dedupeInts(touched), nil
}

// dropInboundFromGroupTx removes the inbound from the group's accounts, but ONLY from
// the ones no other group still claims it for. An account in two scoped groups keeps
// the inbound when one of them detaches; that is the "never disconnect on the last
// claim" guarantee. Returns the inbound ids the projections touched.
func (s *InboundClientGroupService) dropInboundFromGroupTx(tx *gorm.DB, inboundId, groupId int) ([]int, error) {
	var members []model.ClientGroupMember
	if err := tx.Where("group_id = ?", groupId).Find(&members).Error; err != nil {
		return nil, err
	}
	touched := []int{}
	for _, m := range members {
		// Still claimed by ANY group the account is in (other than, or including, this
		// one)? If some OTHER group also scopes this inbound, the account keeps it.
		otherClaim, err := s.otherGroupClaimTx(tx, m.AccountId, inboundId, groupId)
		if err != nil {
			return nil, err
		}
		if otherClaim {
			continue
		}
		// No remaining claim: drop the membership if the account had it.
		ids, err := s.removeMembershipTx(tx, m.AccountId, inboundId)
		if err != nil {
			return nil, err
		}
		touched = appendInts(touched, ids)
	}
	return dedupeInts(touched), nil
}

// otherGroupClaimTx reports whether any group OTHER than excludeGroupId still scopes
// this inbound for the account.
func (s *InboundClientGroupService) otherGroupClaimTx(tx *gorm.DB, accountId, inboundId, excludeGroupId int) (bool, error) {
	var memberGroups []model.ClientGroupMember
	if err := tx.Where("account_id = ?", accountId).Find(&memberGroups).Error; err != nil {
		return false, err
	}
	for _, mg := range memberGroups {
		if mg.GroupId == excludeGroupId {
			continue
		}
		var claims int64
		if err := tx.Model(&model.InboundClientGroup{}).
			Where("inbound_id = ? AND group_id = ?", inboundId, mg.GroupId).
			Count(&claims).Error; err != nil {
			return false, err
		}
		if claims > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ensureMembershipTx adds the account->inbound membership if absent, minting the
// protocol credentials and pool slot SetMemberships would. It reuses SetMemberships
// with the account's full wanted set (current plus the new one) so nothing existing
// is dropped, then runs ProjectAccount so the inbound's settings blob actually carries
// the client - SetMemberships only writes the account_inbounds row, and Xray reads the
// client from the settings JSON, so the projection is what makes it served. Returns
// the inbound ids the projection touched (empty when nothing changed).
func (s *InboundClientGroupService) ensureMembershipTx(tx *gorm.DB, accountId, inboundId int) ([]int, error) {
	var rows []model.AccountInbound
	if err := tx.Where("account_id = ?", accountId).Find(&rows).Error; err != nil {
		return nil, err
	}
	want := map[int]bool{inboundId: true}
	for _, r := range rows {
		want[r.InboundId] = true
	}
	ids := make([]int, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	var acc AccountService
	if err := acc.SetMemberships(tx, accountId, ids); err != nil {
		return nil, err
	}
	return acc.ProjectAccount(tx, accountId)
}

// removeMembershipTx drops the account->inbound membership if present, then re-projects
// the account so the inbound's settings blob no longer carries the client. Returns the
// inbound ids the projection touched.
func (s *InboundClientGroupService) removeMembershipTx(tx *gorm.DB, accountId, inboundId int) ([]int, error) {
	var rows []model.AccountInbound
	if err := tx.Where("account_id = ?", accountId).Find(&rows).Error; err != nil {
		return nil, err
	}
	keep := make([]int, 0, len(rows))
	found := false
	for _, r := range rows {
		if r.InboundId == inboundId {
			found = true
			continue
		}
		keep = append(keep, r.InboundId)
	}
	if !found {
		return nil, nil
	}
	var acc AccountService
	if err := acc.SetMemberships(tx, accountId, keep); err != nil {
		return nil, err
	}
	return acc.ProjectAccount(tx, accountId)
}

// ProjectAccountToGroupInbounds projects an account onto every inbound its current
// groups carry. Called when the account's group membership GROWS. Idempotent.
// Returns the inbound ids whose settings changed.
func (s *InboundClientGroupService) ProjectAccountToGroupInbounds(accountId int) ([]int, error) {
	touched := []int{}
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		want := s.inboundsForAccountDB(tx, accountId)
		for _, inboundId := range want {
			ids, err := s.ensureMembershipTx(tx, accountId, inboundId)
			if err != nil {
				return err
			}
			touched = appendInts(touched, ids)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dedupeInts(touched), nil
}

// DropAccountFromGroupInbounds removes the account from the inbounds it reached ONLY
// through the groups in removedGroupIds, and only when no remaining group still
// claims them. Hand-added memberships (no group at all scopes the inbound) are never
// touched, and an inbound scoped by a group the account still keeps is kept. This is
// the "never disconnect on the last claim" rule made precise: the removal set is
// scoped to the groups actually lost, not "everything not in a group".
// Returns the inbound ids whose settings changed.
func (s *InboundClientGroupService) DropAccountFromGroupInbounds(accountId int, removedGroupIds []int) ([]int, error) {
	if len(removedGroupIds) == 0 {
		return nil, nil
	}
	touched := []int{}
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		var rows []model.AccountInbound
		if err := tx.Where("account_id = ?", accountId).Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			// Only consider an inbound one of the just-lost groups scoped. A hand
			// membership (no group scopes it) is never pulled by a group change.
			if !s.inboundScopedToAnyGroupTx(tx, r.InboundId, removedGroupIds) {
				continue
			}
			// Keep it if any of the account's remaining groups still scopes it - the
			// account is still entitled to it through that group.
			if claimed, _ := s.anyGroupClaimTx(tx, accountId, r.InboundId); claimed {
				continue
			}
			ids, err := s.removeMembershipTx(tx, accountId, r.InboundId)
			if err != nil {
				return err
			}
			touched = appendInts(touched, ids)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dedupeInts(touched), nil
}

// inboundScopedToAnyGroupTx reports whether any group in groupIds scopes the inbound.
func (s *InboundClientGroupService) inboundScopedToAnyGroupTx(tx *gorm.DB, inboundId int, groupIds []int) bool {
	for _, gid := range groupIds {
		var claims int64
		if err := tx.Model(&model.InboundClientGroup{}).
			Where("inbound_id = ? AND group_id = ?", inboundId, gid).
			Count(&claims).Error; err != nil {
			// A read failure must fail closed to the KEEP side: never drop a membership
			// because a count query errored.
			return true
		}
		if claims > 0 {
			return true
		}
	}
	return false
}

// anyGroupClaimTx reports whether ANY of the account's groups scopes the inbound.
func (s *InboundClientGroupService) anyGroupClaimTx(tx *gorm.DB, accountId, inboundId int) (bool, error) {
	var memberGroups []model.ClientGroupMember
	if err := tx.Where("account_id = ?", accountId).Find(&memberGroups).Error; err != nil {
		return false, err
	}
	for _, mg := range memberGroups {
		var claims int64
		if err := tx.Model(&model.InboundClientGroup{}).
			Where("inbound_id = ? AND group_id = ?", inboundId, mg.GroupId).
			Count(&claims).Error; err != nil {
			return false, err
		}
		if claims > 0 {
			return true, nil
		}
	}
	return false, nil
}

// DeleteGroupInboundLinks removes a group's inbound scope when the group itself is
// deleted: for each inbound the group scoped, the group's accounts lose it only when
// no other group still claims it for them (the "never disconnect on the last claim"
// rule). The link rows are dropped last so the drop logic sees the group's scope
// while deciding.
func (s *InboundClientGroupService) DeleteGroupInboundLinks(groupId int) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var links []model.InboundClientGroup
		if err := tx.Where("group_id = ?", groupId).Find(&links).Error; err != nil {
			return err
		}
		for _, l := range links {
			if _, err := s.dropInboundFromGroupTx(tx, l.InboundId, groupId); err != nil {
				return err
			}
		}
		return tx.Where("group_id = ?", groupId).Delete(&model.InboundClientGroup{}).Error
	})
}

func dedupeInts(in []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func appendInts(dst, src []int) []int {
	return append(dst, src...)
}
