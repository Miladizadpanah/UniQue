package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// InboundGroupService stores the operator's groups over the inbound list.
//
// A group is a SAVED VIEW, not a container: it changes nothing about an inbound, no
// protocol reads it, and an inbound in no group behaves exactly as before. That is
// what makes the feature safe to add to a live panel and what keeps every existing
// write path (add, edit, delete, Xray reload) completely unaware of it.
type InboundGroupService struct{}

// InboundGroupView is one group joined with its members, which is the only shape any
// caller wants: the group and the membership map live in two tables and every reader
// needs both at once.
type InboundGroupView struct {
	Id         int    `json:"id"`
	Name       string `json:"name"`
	ParentId   int    `json:"parentId"`
	SortOrder  int    `json:"sortOrder"`
	InboundIds []int  `json:"inboundIds"`
}

// ErrInboundGroupName is returned when a group is saved without a name.
var ErrInboundGroupName = errors.New("inbound group name is required")

// ErrInboundGroupCycle is returned when a group is asked to become its own ancestor.
// Refused rather than silently reparented: a cycle would make the tree unrenderable
// and hang any walk that tried.
var ErrInboundGroupCycle = errors.New("an inbound group cannot be nested inside itself or one of its subgroups")

// List returns every group with its members, in display order.
func (s *InboundGroupService) List() ([]InboundGroupView, error) {
	db := database.GetDB()
	var groups []model.InboundGroup
	if err := db.Order("sort_order asc, id asc").Find(&groups).Error; err != nil {
		return nil, err
	}
	var members []model.InboundGroupMember
	if err := db.Order("sort_order asc, inbound_id asc").Find(&members).Error; err != nil {
		return nil, err
	}
	byGroup := make(map[int][]int, len(groups))
	for _, m := range members {
		byGroup[m.GroupId] = append(byGroup[m.GroupId], m.InboundId)
	}
	out := make([]InboundGroupView, 0, len(groups))
	for _, g := range groups {
		ids := byGroup[g.Id]
		if ids == nil {
			ids = []int{}
		}
		out = append(out, InboundGroupView{
			Id: g.Id, Name: g.Name, ParentId: g.ParentId, SortOrder: g.SortOrder, InboundIds: ids,
		})
	}
	return out, nil
}

// Create makes one group holding the given inbounds.
func (s *InboundGroupService) Create(name string, parentId int, inboundIds []int) (*model.InboundGroup, error) {
	if err := validGroupName(name); err != nil {
		return nil, err
	}
	db := database.GetDB()
	if parentId != 0 {
		if err := s.exists(db, parentId); err != nil {
			return nil, err
		}
	}
	var group model.InboundGroup
	err := db.Transaction(func(tx *gorm.DB) error {
		group = model.InboundGroup{Name: name, ParentId: parentId}
		if err := tx.Create(&group).Error; err != nil {
			return err
		}
		return replaceGroupMembers(tx, group.Id, inboundIds)
	})
	if err != nil {
		return nil, err
	}
	return &group, nil
}

// Update renames a group, moves it under another group and replaces its members.
func (s *InboundGroupService) Update(id int, name string, parentId int, inboundIds []int) error {
	if err := validGroupName(name); err != nil {
		return err
	}
	db := database.GetDB()
	if err := s.exists(db, id); err != nil {
		return err
	}
	if parentId != 0 {
		if err := s.exists(db, parentId); err != nil {
			return err
		}
		if err := s.assertNoCycle(db, id, parentId); err != nil {
			return err
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.InboundGroup{}).Where("id = ?", id).
			Updates(map[string]any{"name": name, "parent_id": parentId}).Error; err != nil {
			return err
		}
		return replaceGroupMembers(tx, id, inboundIds)
	})
}

// Delete removes a group. Its SUBGROUPS are reparented to its own parent rather than
// deleted with it, and that is the whole point: a group is a view, so deleting a
// heading must never take the headings under it (or, above all, the inbounds) away.
func (s *InboundGroupService) Delete(id int) error {
	db := database.GetDB()
	if err := s.exists(db, id); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var group model.InboundGroup
		if err := tx.First(&group, id).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.InboundGroup{}).Where("parent_id = ?", id).
			Update("parent_id", group.ParentId).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&model.InboundGroupMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.InboundGroup{}, id).Error
	})
}

// Copy duplicates a group, its members and (recursively) its subgroups under a new
// name. Recursing is deliberate: copying a parent that silently dropped the tree
// under it would be a copy of something the operator never saw as a group.
func (s *InboundGroupService) Copy(id int, name string) (*model.InboundGroup, error) {
	if err := validGroupName(name); err != nil {
		return nil, err
	}
	db := database.GetDB()
	if err := s.exists(db, id); err != nil {
		return nil, err
	}
	var created model.InboundGroup
	err := db.Transaction(func(tx *gorm.DB) error {
		var src model.InboundGroup
		if err := tx.First(&src, id).Error; err != nil {
			return err
		}
		var err error
		created, err = copyGroupTx(tx, src, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// copyGroupTx deep-copies one group. Root names are the caller's; descendant names
// are carried over unchanged, since their meaning is relative to the parent.
func copyGroupTx(tx *gorm.DB, src model.InboundGroup, name string) (model.InboundGroup, error) {
	clone := model.InboundGroup{Name: name, ParentId: src.ParentId, SortOrder: src.SortOrder}
	if err := tx.Create(&clone).Error; err != nil {
		return clone, err
	}
	var members []model.InboundGroupMember
	if err := tx.Where("group_id = ?", src.Id).Find(&members).Error; err != nil {
		return clone, err
	}
	for _, m := range members {
		row := model.InboundGroupMember{GroupId: clone.Id, InboundId: m.InboundId, SortOrder: m.SortOrder}
		if err := tx.Create(&row).Error; err != nil {
			return clone, err
		}
	}
	var children []model.InboundGroup
	if err := tx.Where("parent_id = ?", src.Id).Order("sort_order asc, id asc").Find(&children).Error; err != nil {
		return clone, err
	}
	for _, child := range children {
		if _, err := copyGroupTx(tx, child, child.Name); err != nil {
			return clone, err
		}
	}
	return clone, nil
}

// Reorder sets group order from the id list's positions, matching ReorderInbounds.
// Ids the caller omitted keep their existing order at the end.
func (s *InboundGroupService) Reorder(ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	db := database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		for i, id := range ids {
			if err := tx.Model(&model.InboundGroup{}).Where("id = ?", id).
				Update("sort_order", i+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func validGroupName(name string) error {
	if len([]rune(name)) == 0 {
		return ErrInboundGroupName
	}
	return nil
}

func (s *InboundGroupService) exists(db *gorm.DB, id int) error {
	var group model.InboundGroup
	return db.First(&group, id).Error
}

// assertNoCycle refuses to make `parentId` the parent of `id` when that would create
// a loop - i.e. when `id` is `parentId` itself or sits above it in the tree.
func (s *InboundGroupService) assertNoCycle(db *gorm.DB, id int, parentId int) error {
	for cur := parentId; cur != 0; {
		if cur == id {
			return ErrInboundGroupCycle
		}
		var group model.InboundGroup
		if err := db.First(&group, cur).Error; err != nil {
			// A dangling parent breaks the walk; treating it as the root is the safe
			// direction (the update then simply reparents under a missing id, which
			// the UI shows as top level).
			break
		}
		cur = group.ParentId
	}
	return nil
}

// replaceGroupMembers sets a group's membership to exactly inboundIds.
func replaceGroupMembers(tx *gorm.DB, groupId int, inboundIds []int) error {
	if err := tx.Where("group_id = ?", groupId).Delete(&model.InboundGroupMember{}).Error; err != nil {
		return err
	}
	for i, inboundId := range inboundIds {
		row := model.InboundGroupMember{GroupId: groupId, InboundId: inboundId, SortOrder: i + 1}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
