package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const UnaChannelMirrorContractVersion = "una-channel-mirror.v2"

var (
	ErrUnaChannelMirrorInvalid  = errors.New("una channel mirror request invalid")
	ErrUnaChannelMirrorConflict = errors.New("una channel mirror conflict")
)

type UnaChannelMirrorService struct {
	DB  *gorm.DB
	Now func() time.Time
}

type unaChannelSettings struct {
	Group string `json:"group"`
}

type unaChannelMetadata struct {
	Provider       string          `json:"provider"`
	AdapterType    string          `json:"adapter_type"`
	DesiredVersion uint64          `json:"desired_version"`
	Settings       json.RawMessage `json:"settings"`
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
	settings, metadata, err := decodeUnaChannelSettings(req)
	if err != nil {
		return dto.UnaChannelMirrorResponse{}, err
	}
	response := dto.UnaChannelMirrorResponse{ContractVersion: UnaChannelMirrorContractVersion, DesiredVersion: req.DesiredVersion}
	changed := false
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.UnaChannelMirror
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("mapping_id = ?", req.MappingID).First(&existing).Error
		switch {
		case findErr == nil:
			if req.DesiredVersion < existing.DesiredVersion {
				response.ChannelID = uint64(existing.ChannelID)
				return nil
			}
			if req.DesiredVersion == existing.DesiredVersion {
				if existing.RequestFingerprint != fingerprint || (req.ChannelID != nil && existing.ChannelID != int(*req.ChannelID)) {
					return ErrUnaChannelMirrorConflict
				}
				response.ChannelID = uint64(existing.ChannelID)
				return nil
			}
		case errors.Is(findErr, gorm.ErrRecordNotFound):
		default:
			return findErr
		}

		channelID := 0
		if req.ChannelID != nil {
			channelID = int(*req.ChannelID)
		} else if findErr == nil {
			channelID = existing.ChannelID
		}
		if findErr == nil && existing.ChannelID != channelID {
			return ErrUnaChannelMirrorConflict
		}
		now := s.Now().UTC().Unix()
		var channel model.Channel
		if channelID == 0 {
			channel = model.Channel{
				Type: constant.ChannelTypeOpenAI, Key: req.Credential, Name: req.ChannelName,
				Status: common.ChannelStatusManuallyDisabled, Weight: uintPointer(uint(req.Weight)),
				CreatedTime: now, BaseURL: stringPointer(req.BaseURL), Models: "", Group: settings.Group,
				Priority: int64Pointer(req.Priority), AutoBan: intPointer(1), OtherInfo: metadata,
			}
			if err := tx.Create(&channel).Error; err != nil {
				return err
			}
			channelID = channel.Id
		} else {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&channel, "id = ?", channelID).Error; err != nil {
				return err
			}
			channel.Type, channel.Key, channel.Name = constant.ChannelTypeOpenAI, req.Credential, req.ChannelName
			channel.BaseURL, channel.Weight, channel.Priority, channel.Group, channel.OtherInfo = stringPointer(req.BaseURL), uintPointer(uint(req.Weight)), int64Pointer(req.Priority), settings.Group, metadata
			if err := tx.Model(&model.Channel{}).Where("id = ?", channelID).Select("type", "key", "name", "base_url", "weight", "priority", "group", "other_info").Updates(&channel).Error; err != nil {
				return err
			}
		}
		response.ChannelID = uint64(channelID)
		var before []model.UnaChannelMirror
		if err := tx.Where("channel_id = ?", channelID).Find(&before).Error; err != nil {
			return err
		}
		record := model.UnaChannelMirror{
			MappingID: req.MappingID, ChannelID: channelID, Model: req.Model,
			ChannelName: req.ChannelName, UpstreamModel: req.UpstreamModel,
			Priority: req.Priority, Weight: req.Weight, Enabled: req.Enabled,
			DesiredVersion: req.DesiredVersion, RequestFingerprint: fingerprint,
			CreatedTime: now, UpdatedTime: now,
		}
		if findErr == nil {
			record.ID, record.CreatedTime = existing.ID, existing.CreatedTime
			if err := tx.Model(&model.UnaChannelMirror{}).Where("id = ?", existing.ID).Select(
				"model", "channel_name", "upstream_model", "priority", "weight", "enabled", "desired_version", "request_fingerprint", "updated_time",
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
		status := common.ChannelStatusManuallyDisabled
		if hasEnabledUnaMirror(after) {
			status = common.ChannelStatusEnabled
		}
		channel.Models, channel.ModelMapping, channel.Status = models, stringPointer(string(mappingJSON)), status
		if err := tx.Model(&model.Channel{}).Where("id = ?", channelID).Select("models", "model_mapping", "status").Updates(&channel).Error; err != nil {
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
	req.ContractVersion, req.MappingID, req.Model = strings.TrimSpace(req.ContractVersion), strings.TrimSpace(req.MappingID), strings.TrimSpace(req.Model)
	req.ChannelName, req.Provider, req.AdapterType = strings.TrimSpace(req.ChannelName), strings.ToLower(strings.TrimSpace(req.Provider)), strings.ToLower(strings.TrimSpace(req.AdapterType))
	req.BaseURL, req.Credential, req.UpstreamModel, req.CostRatio = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"), strings.TrimSpace(req.Credential), strings.TrimSpace(req.UpstreamModel), strings.TrimSpace(req.CostRatio)
	return req
}

func validateUnaChannelMirrorRequest(req dto.UnaChannelMirrorRequest) error {
	if req.ContractVersion != UnaChannelMirrorContractVersion || req.DesiredVersion == 0 || req.ChannelDesiredVersion == 0 || (req.ChannelID != nil && (*req.ChannelID == 0 || *req.ChannelID > uint64(^uint(0)>>1))) {
		return ErrUnaChannelMirrorInvalid
	}
	if !validUnaText(req.MappingID, 64) || !validUnaText(req.Model, 100) || !validUnaText(req.ChannelName, 100) || !validUnaText(req.Provider, 50) || req.AdapterType != "openai-compatible" || !validUnaText(req.UpstreamModel, 150) || len(req.Credential) == 0 || len(req.Credential) > 8192 {
		return ErrUnaChannelMirrorInvalid
	}
	if strings.ContainsAny(req.Model, ",{}") || strings.ContainsAny(req.UpstreamModel, "{}") || req.Priority < 0 || req.Weight < 1 || uint64(req.Weight) > uint64(^uint(0)) || !validUnaCostRatio(req.CostRatio) {
		return ErrUnaChannelMirrorInvalid
	}
	target, err := url.Parse(req.BaseURL)
	if err != nil || target.Hostname() == "" || (target.Scheme != "https" && target.Scheme != "http") || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return ErrUnaChannelMirrorInvalid
	}
	if len(req.Settings) == 0 || len(req.Settings) > 4096 || !json.Valid(req.Settings) {
		return ErrUnaChannelMirrorInvalid
	}
	var object map[string]any
	if json.Unmarshal(req.Settings, &object) != nil || object == nil {
		return ErrUnaChannelMirrorInvalid
	}
	return nil
}

func decodeUnaChannelSettings(req dto.UnaChannelMirrorRequest) (unaChannelSettings, string, error) {
	settings := unaChannelSettings{Group: "default"}
	if err := json.Unmarshal(req.Settings, &settings); err != nil {
		return unaChannelSettings{}, "", ErrUnaChannelMirrorInvalid
	}
	settings.Group = strings.TrimSpace(settings.Group)
	if settings.Group == "" {
		settings.Group = "default"
	}
	if !validUnaText(settings.Group, 64) || strings.ContainsAny(settings.Group, "{}") {
		return unaChannelSettings{}, "", ErrUnaChannelMirrorInvalid
	}
	payload, err := json.Marshal(unaChannelMetadata{Provider: req.Provider, AdapterType: req.AdapterType, DesiredVersion: req.ChannelDesiredVersion, Settings: req.Settings})
	if err != nil {
		return unaChannelSettings{}, "", err
	}
	return settings, string(payload), nil
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
	return !(whole == 0 && strings.Trim(fraction, "0") == "")
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
func uintPointer(value uint) *uint       { return &value }
func int64Pointer(value int64) *int64    { return &value }
func intPointer(value int) *int          { return &value }
