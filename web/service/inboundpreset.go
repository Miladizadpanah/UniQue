package service

import (
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

// InboundPresetService stores saved inbound "configs": the Add/Edit dialog's own
// JSON, kept under a name the operator chose, so an inbound they build once can be
// rebuilt on another port (or another panel) without re-entering every setting.
//
// A preset is a TEMPLATE. Nothing here touches Xray, an inbound row or a client;
// applying one only fills the form, and the ordinary save path is still the only
// thing that writes an inbound. That keeps a preset from becoming a second write
// path around the permission and ownership checks every real write goes through.
type InboundPresetService struct{}

// ErrInboundPresetName is returned when a preset is saved without a name. The name
// is also the identity a re-save replaces, so it cannot be left blank or the second
// save of an unnamed preset would silently overwrite the first.
var ErrInboundPresetName = errors.New("inbound preset name is required")

// ErrInboundPresetContent is returned when there is nothing to save.
var ErrInboundPresetContent = errors.New("inbound preset content is required")

// List returns every preset, oldest first, so the picker's order is stable and new
// presets append rather than jumping to the top under the operator's cursor.
func (s *InboundPresetService) List() ([]model.InboundPreset, error) {
	var presets []model.InboundPreset
	err := database.GetDB().Model(model.InboundPreset{}).Order("id asc").Find(&presets).Error
	if err != nil {
		return nil, err
	}
	return presets, nil
}

// Save stores one preset, replacing an existing preset of the SAME NAME.
//
// Replace-by-name rather than always-insert is deliberate: the picker is a short
// list an operator scans, and allowing the same name twice would make it
// ambiguous which of the two a click applies, with no way to tell them apart.
func (s *InboundPresetService) Save(name, content string) (*model.InboundPreset, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrInboundPresetName
	}
	if strings.TrimSpace(content) == "" {
		return nil, ErrInboundPresetContent
	}

	db := database.GetDB()
	var existing model.InboundPreset
	err := db.Model(model.InboundPreset{}).Where("name = ?", name).First(&existing).Error
	if err == nil {
		if err := db.Model(model.InboundPreset{}).Where("id = ?", existing.Id).
			Update("content", content).Error; err != nil {
			return nil, err
		}
		existing.Content = content
		return &existing, nil
	}

	preset := &model.InboundPreset{Name: name, Content: content}
	if err := db.Create(preset).Error; err != nil {
		return nil, err
	}
	return preset, nil
}

// Delete removes one preset by id. Deleting an id that is already gone is not an
// error: the operator asked for it to be absent, and it is.
func (s *InboundPresetService) Delete(id int) error {
	return database.GetDB().Delete(&model.InboundPreset{}, id).Error
}
