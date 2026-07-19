package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const UnaChannelMirrorContractVersion = "una-channel-mirror.v1"

var (
	ErrUnaChannelMirrorInvalid  = errors.New("una channel mirror request invalid")
	ErrUnaChannelMirrorConflict = errors.New("una channel mirror conflict")
)

type UnaChannelMirrorService struct {
	DB  *gorm.DB
	Now func() time.Time
}

func NewUnaChannelMirrorService() (*UnaChannelMirrorService, error) {
	if model.DB == nil {
		return nil, errors.New("una channel mirror database is not configured")
	}
	return &UnaChannelMirrorService{DB: model.DB, Now: time.Now}, nil
}

func (s *UnaChannelMirrorService) Apply(ctx context.Context, req dto.UnaChannelMirrorRequest) (dto.UnaChannelMirrorResponse, error) {
	req = normalizeUnaChannelMirrorRequest(req)
	if err := validateUnaChannelMirrorRequest(req); err != nil {
		return dto.UnaChannelMirrorResponse{}, err
	}
	if s == nil || s.DB == nil || s.Now == nil {
		return dto.UnaChannelMirrorResponse{}, errors.New("una channel mirror service is not configured")
	}

	fingerprint, err := unaChannelMirrorFingerprint(req)
	if err != nil {
		return dto.UnaChannelMirrorResponse{}, err
	}
	channelID := int(*req.ChannelID)
	response := dto.UnaChannelMirrorResponse{
		ContractVersion: UnaChannelMirrorContractVersion,
		ChannelID:       *req.ChannelID,
		DesiredVersion:  req.DesiredVersion,
	}

	changed := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.UnaChannelMirror
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("mapping_id = ?", req.MappingID).First(&existing).Error
		switch {
		case findErr == nil:
			if req.DesiredVersion < existing.DesiredVersion {
				// 陈旧版本只确认 no-op，不能回滚已经应用的新版本。
				response.ChannelID = uint64(existing.ChannelID)
				return nil
			}
			if req.DesiredVersion == existing.DesiredVersion {
				if existing.RequestFingerprint != fingerprint || existing.ChannelID != channelID {
					return ErrUnaChannelMirrorConflict
				}
				return nil
			}
			if existing.ChannelID != channelID {
				// 切换物理渠道必须创建新的 mapping，避免一次请求跨渠道修改两套能力。
				return ErrUnaChannelMirrorConflict
			}
		case errors.Is(findErr, gorm.ErrRecordNotFound):
		default:
			return findErr
		}

		var channel model.Channel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&channel, "id = ?", channelID).Error; err != nil {
			return err
		}

		var before []model.UnaChannelMirror
		if err := tx.Where("channel_id = ?", channelID).Find(&before).Error; err != nil {
			return err
		}

		now := s.Now().UTC().Unix()
		record := model.UnaChannelMirror{
			MappingID: req.MappingID, ChannelID: channelID, Model: req.Model,
			ChannelName: req.ChannelName, UpstreamModel: req.UpstreamModel,
			Priority: req.Priority, Weight: req.Weight, Enabled: req.Enabled,
			DesiredVersion: req.DesiredVersion, RequestFingerprint: fingerprint,
			CreatedTime: now, UpdatedTime: now,
		}
		if findErr == nil {
			record.ID = existing.ID
			record.CreatedTime = existing.CreatedTime
			if err := tx.Model(&model.UnaChannelMirror{}).Where("id = ?", existing.ID).Select(
				"model", "channel_name", "upstream_model", "priority", "weight", "enabled",
				"desired_version", "request_fingerprint", "updated_time",
			).Updates(&record).Error; err != nil {
				return err
			}
		} else if err := tx.Create(&record).Error; err != nil {
			return err
		}

		var after []model.UnaChannelMirror
		if err := tx.Where("channel_id = ?", channelID).Order("mapping_id ASC").Find(&after).Error; err != nil {
			return err
		}
		models, mapping, err := mergeUnaManagedModels(channel.Models, channel.GetModelMapping(), before, after)
		if err != nil {
			return err
		}
		mappingJSON, err := json.Marshal(mapping)
		if err != nil {
			return err
		}
		status := channel.Status

		if hasEnabledUnaMirror(after) {
			status = common.ChannelStatusEnabled
		} else if strings.TrimSpace(models) == "" {
			status = common.ChannelStatusManuallyDisabled
		}
		channel.Name = req.ChannelName
		channel.Models = models
		channel.ModelMapping = stringPointer(string(mappingJSON))
		channel.Status = status
		if err := tx.Model(&model.Channel{}).Where("id = ?", channelID).Select(
			"name", "models", "model_mapping", "status",
		).Updates(&channel).Error; err != nil {
			return err
		}
		if err := rebuildUnaManagedAbilities(tx, channel, before, after); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return dto.UnaChannelMirrorResponse{}, err
	}
	if changed {
		model.InitChannelCache()
		ResetProxyClientCache()
	}
	return response, nil
}

func normalizeUnaChannelMirrorRequest(req dto.UnaChannelMirrorRequest) dto.UnaChannelMirrorRequest {
	req.ContractVersion = strings.TrimSpace(req.ContractVersion)
	req.MappingID = strings.TrimSpace(req.MappingID)
	req.Model = strings.TrimSpace(req.Model)
	req.ChannelName = strings.TrimSpace(req.ChannelName)
	req.UpstreamModel = strings.TrimSpace(req.UpstreamModel)
	req.CostRatio = strings.TrimSpace(req.CostRatio)
	return req
}

func validateUnaChannelMirrorRequest(req dto.UnaChannelMirrorRequest) error {
	if req.ContractVersion != UnaChannelMirrorContractVersion || req.DesiredVersion == 0 || req.ChannelID == nil || *req.ChannelID == 0 || *req.ChannelID > uint64(^uint(0) >> 1) {
		return ErrUnaChannelMirrorInvalid
	}
	if !validUnaText(req.MappingID, 64) || !validUnaText(req.Model, 100) || !validUnaText(req.ChannelName, 100) || !validUnaText(req.UpstreamModel, 150) {
		return ErrUnaChannelMirrorInvalid
	}
	if strings.ContainsAny(req.Model, ",{}") || strings.ContainsAny(req.UpstreamModel, "{}") || req.Priority < 0 || req.Weight < 1 || uint64(req.Weight) > uint64(^uint(0)) {
		return ErrUnaChannelMirrorInvalid
	}
	if !validUnaCostRatio(req.CostRatio) {
		return ErrUnaChannelMirrorInvalid
	}
	return nil
}

func validUnaText(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validUnaCostRatio(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 2 {
		return false
	}
	whole, err := strconv.Atoi(parts[0])
	if err != nil || whole < 0 || whole > 99 {
		return false
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > 4 {
			return false
		}
		if _, err := strconv.Atoi(fraction); err != nil {
			return false
		}
	}
	allZero := whole == 0 && strings.Trim(fraction, "0") == ""
	return !allZero
}

func unaChannelMirrorFingerprint(req dto.UnaChannelMirrorRequest) (string, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func mergeUnaManagedModels(currentModels, currentMapping string, before, after []model.UnaChannelMirror) (string, map[string]string, error) {
	models := make(map[string]struct{})
	for _, value := range strings.Split(currentModels, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			models[value] = struct{}{}
		}
	}
	mapping := make(map[string]string)
	if strings.TrimSpace(currentMapping) != "" {
		if err := json.Unmarshal([]byte(currentMapping), &mapping); err != nil {
			return "", nil, ErrUnaChannelMirrorConflict
		}
	}
	for _, item := range before {
		delete(models, item.Model)
		delete(mapping, item.Model)
	}
	for _, item := range after {
		if !item.Enabled {
			continue
		}
		models[item.Model] = struct{}{}
		mapping[item.Model] = item.UpstreamModel
	}
	ordered := make([]string, 0, len(models))
	for value := range models {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	return strings.Join(ordered, ","), mapping, nil
}

func rebuildUnaManagedAbilities(tx *gorm.DB, channel model.Channel, before, after []model.UnaChannelMirror) error {
	managed := make(map[string]struct{})
	for _, item := range before {
		managed[item.Model] = struct{}{}
	}
	for _, item := range after {
		managed[item.Model] = struct{}{}
	}
	if len(managed) > 0 {
		models := make([]string, 0, len(managed))
		for value := range managed {
			models = append(models, value)
		}
		if err := tx.Where("channel_id = ? AND model IN ?", channel.Id, models).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
	}
	groups := channel.GetGroups()
	abilities := make([]model.Ability, 0, len(groups)*len(after))
	seen := make(map[string]struct{})
	for _, item := range after {
		if !item.Enabled {
			continue
		}
		for _, group := range groups {
			key := group + "\x00" + item.Model
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			priority := item.Priority
			abilities = append(abilities, model.Ability{
				Group: group, Model: item.Model, ChannelId: channel.Id, Enabled: true,
				Priority: &priority, Weight: uint(item.Weight), Tag: channel.Tag,
			})
		}
	}
	if len(abilities) == 0 {
		return nil
	}
	return tx.Create(&abilities).Error
}
func hasEnabledUnaMirror(values []model.UnaChannelMirror) bool {
	for _, value := range values {
		if value.Enabled {
			return true
		}
	}
	return false
}

func stringPointer(value string) *string { return &value }
