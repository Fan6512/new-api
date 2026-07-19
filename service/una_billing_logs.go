package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const UnaBillingLogsContractVersion = "una-billing-logs.v1"

var (
	ErrUnaBillingLogsInvalid     = errors.New("una billing logs request invalid")
	ErrUnaBillingLogsUnsupported = errors.New("una billing logs backend unsupported")
	unaSafeErrorCode             = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

type UnaBillingLogQuery struct {
	StartTimestamp int64
	EndTimestamp   int64
	AfterCreatedAt *int64
	AfterID        string
	Limit          int
}

type UnaBillingLogReader struct {
	DB    *gorm.DB
	LogDB *gorm.DB
}

func NewUnaBillingLogReader() (*UnaBillingLogReader, error) {
	if model.DB == nil || model.LOG_DB == nil {
		return nil, errors.New("una billing log databases are not configured")
	}
	return &UnaBillingLogReader{DB: model.DB, LogDB: model.LOG_DB}, nil
}

func (r *UnaBillingLogReader) List(ctx context.Context, query UnaBillingLogQuery) (dto.UnaBillingLogResponse, error) {
	if r == nil || r.DB == nil || r.LogDB == nil || query.StartTimestamp <= 0 || query.EndTimestamp < query.StartTimestamp || query.Limit < 1 || query.Limit > 1000 {
		return dto.UnaBillingLogResponse{}, ErrUnaBillingLogsInvalid
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// 当前 ClickHouse 展示 ID 不是持久稳定游标，必须 fail-closed。
		return dto.UnaBillingLogResponse{}, ErrUnaBillingLogsUnsupported
	}

	dbQuery := r.LogDB.WithContext(ctx).Where("type = ? AND created_at >= ? AND created_at <= ?", model.LogTypeConsume, query.StartTimestamp, query.EndTimestamp)
	if query.AfterCreatedAt != nil {
		afterID, err := parseUnaBillingLogID(query.AfterID)
		if err != nil || *query.AfterCreatedAt < query.StartTimestamp || *query.AfterCreatedAt > query.EndTimestamp {
			return dto.UnaBillingLogResponse{}, ErrUnaBillingLogsInvalid
		}
		dbQuery = dbQuery.Where("created_at > ? OR (created_at = ? AND id > ?)", *query.AfterCreatedAt, *query.AfterCreatedAt, afterID)
	} else if query.AfterID != "" {
		return dto.UnaBillingLogResponse{}, ErrUnaBillingLogsInvalid
	}

	var logs []model.Log
	if err := dbQuery.Order("created_at ASC, id ASC").Limit(query.Limit + 1).Find(&logs).Error; err != nil {
		return dto.UnaBillingLogResponse{}, err
	}
	hasMore := len(logs) > query.Limit
	if hasMore {
		logs = logs[:query.Limit]
	}
	channelNames, err := r.channelNames(ctx, logs)
	if err != nil {
		return dto.UnaBillingLogResponse{}, err
	}

	rows := make([]dto.UnaBillingLogRow, 0, len(logs))
	for i := range logs {
		row, err := unaBillingLogRow(logs[i], channelNames)
		if err != nil {
			return dto.UnaBillingLogResponse{}, err
		}
		rows = append(rows, row)
	}
	response := dto.UnaBillingLogResponse{
		ContractVersion: UnaBillingLogsContractVersion,
		Data:            rows,
		HasMore:         hasMore,
	}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		response.NextCursor = &dto.UnaBillingLogCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return response, nil
}

func (r *UnaBillingLogReader) channelNames(ctx context.Context, logs []model.Log) (map[int]string, error) {
	ids := make([]int, 0)
	seen := make(map[int]struct{})
	for _, item := range logs {
		if item.ChannelId <= 0 {
			continue
		}
		if _, ok := seen[item.ChannelId]; ok {
			continue
		}
		seen[item.ChannelId] = struct{}{}
		ids = append(ids, item.ChannelId)
	}
	result := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var channels []struct {
		ID   int    `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := r.DB.WithContext(ctx).Table("channels").Select("id, name").Where("id IN ?", ids).Find(&channels).Error; err != nil {
		return nil, err
	}
	for _, channel := range channels {
		result[channel.ID] = channel.Name
	}
	return result, nil
}

func unaBillingLogRow(item model.Log, channelNames map[int]string) (dto.UnaBillingLogRow, error) {
	if item.Id <= 0 || item.CreatedAt <= 0 || item.TokenId <= 0 || item.UseTime < 0 || int64(item.UseTime) > int64(^uint64(0)>>1)/1000 {
		return dto.UnaBillingLogRow{}, ErrUnaBillingLogsInvalid
	}
	other, err := decodeUnaLogOther(item.Other)
	if err != nil {
		return dto.UnaBillingLogRow{}, ErrUnaBillingLogsInvalid
	}
	upstreamModel := strings.TrimSpace(unaOtherString(other, "upstream_model_name"))
	if upstreamModel == "" {
		upstreamModel = strings.TrimSpace(item.ModelName)
	}
	requestPath := strings.TrimSpace(unaOtherString(other, "request_path"))
	if !validUnaLogText(upstreamModel, 100, false) || !validUnaLogText(requestPath, 200, true) {
		return dto.UnaBillingLogRow{}, ErrUnaBillingLogsInvalid
	}

	prompt := int64(item.PromptTokens)
	completion := int64(item.CompletionTokens)
	cached, hasCached, err := unaOtherInt64(other, "cache_tokens")
	if err != nil {
		return dto.UnaBillingLogRow{}, ErrUnaBillingLogsInvalid
	}
	imageCount, hasImageCount, err := unaOtherInt64(other, "image_count")
	if err != nil {
		return dto.UnaBillingLogRow{}, ErrUnaBillingLogsInvalid
	}
	if prompt < 0 || completion < 0 || (hasCached && cached < 0) || (hasImageCount && imageCount < 0) {
		return dto.UnaBillingLogRow{}, ErrUnaBillingLogsInvalid
	}

	errorCode := strings.TrimSpace(unaOtherString(other, "error_code"))
	if errorCode != "" && !unaSafeErrorCode.MatchString(errorCode) {
		errorCode = "UPSTREAM_ERROR"
	}
	statusCode, _, err := unaOtherInt64(other, "status_code")
	if err != nil {
		return dto.UnaBillingLogRow{}, ErrUnaBillingLogsInvalid
	}
	status := "success"
	lowerCode := strings.ToLower(errorCode)
	if statusCode == 408 || statusCode == 504 || strings.Contains(lowerCode, "timeout") {
		status = "timeout"
	} else if statusCode >= 400 || errorCode != "" {
		status = "failed"
	}

	row := dto.UnaBillingLogRow{
		ID:               formatUnaBillingLogID(item.Id),
		CreatedAt:        item.CreatedAt,
		TokenID:          strconv.Itoa(item.TokenId),
		ModelName:        upstreamModel,
		ChannelName:      channelNames[item.ChannelId],
		RequestPath:      requestPath,
		IsStream:         item.IsStream,
		PromptTokens:     &prompt,
		CompletionTokens: &completion,
		Quota:            strconv.FormatInt(int64(item.Quota), 10) + ".000000",
		LatencyMS:        int64(item.UseTime) * 1000,
		Status:           status,
		ErrorCode:        errorCode,
	}
	if item.ChannelId > 0 {
		value := uint64(item.ChannelId)
		row.ChannelID = &value
	}
	if hasCached {
		row.CachedPromptTokens = &cached
	}
	if hasImageCount {
		row.ImageCount = &imageCount
	}
	return row, nil
}

func decodeUnaLogOther(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected trailing log metadata")
	}
	return value, nil
}

func unaOtherString(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

func unaOtherInt64(values map[string]any, key string) (int64, bool, error) {
	value, ok := values[key]
	if !ok || value == nil {
		return 0, false, nil
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false, err
		}
		return parsed, true, nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, false, err
		}
		return parsed, true, nil
	default:
		return 0, false, errors.New("log metadata integer has invalid type")
	}
}
func formatUnaBillingLogID(id int) string {
	return fmt.Sprintf("%020d", id)
}

func parseUnaBillingLogID(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if len(value) != 20 {
		return 0, ErrUnaBillingLogsInvalid
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, ErrUnaBillingLogsInvalid
	}
	return parsed, nil
}

func validUnaLogText(value string, max int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
