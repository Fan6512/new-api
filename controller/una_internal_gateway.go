package controller

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const unaInternalMaxJSONBody = 64 * 1024

func UnaInternalApplyChannel(c *gin.Context) {
	var req dto.UnaChannelMirrorRequest
	if err := decodeUnaInternalJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	mirror, err := service.NewUnaChannelMirrorService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "una channel mirror is not configured"})
		return
	}
	response, err := mirror.Apply(c.Request.Context(), req)
	if err != nil {
		writeUnaChannelMirrorError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

func writeUnaChannelMirrorError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrUnaChannelMirrorInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
	case errors.Is(err, service.ErrUnaChannelMirrorConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "channel mirror conflict"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "channel mirror failed"})
	}
}

func UnaInternalListBillingLogs(c *gin.Context) {
	query, err := parseUnaBillingLogQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	reader, err := service.NewUnaBillingLogReader()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "una billing logs are not configured"})
		return
	}
	response, err := reader.List(c.Request.Context(), query)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrUnaBillingLogsInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		case errors.Is(err, service.ErrUnaBillingLogsUnsupported):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stable billing cursor is unavailable"})
		default:
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "billing log read failed"})
		}
		return
	}
	c.JSON(http.StatusOK, response)
}

func parseUnaBillingLogQuery(c *gin.Context) (service.UnaBillingLogQuery, error) {
	allowed := map[string]bool{
		"type": true, "start_timestamp": true, "end_timestamp": true,
		"limit": true, "after_created_at": true, "after_id": true,
	}
	for key, values := range c.Request.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return service.UnaBillingLogQuery{}, service.ErrUnaBillingLogsInvalid
		}
	}
	if c.Query("type") != strconv.Itoa(2) {
		return service.UnaBillingLogQuery{}, service.ErrUnaBillingLogsInvalid
	}
	start, err := strconv.ParseInt(strings.TrimSpace(c.Query("start_timestamp")), 10, 64)
	if err != nil || start <= 0 {
		return service.UnaBillingLogQuery{}, service.ErrUnaBillingLogsInvalid
	}
	end, err := strconv.ParseInt(strings.TrimSpace(c.Query("end_timestamp")), 10, 64)
	if err != nil || end < start || end-start > 24*60*60 {
		return service.UnaBillingLogQuery{}, service.ErrUnaBillingLogsInvalid
	}
	limit, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || limit < 1 || limit > 1000 {
		return service.UnaBillingLogQuery{}, service.ErrUnaBillingLogsInvalid
	}
	result := service.UnaBillingLogQuery{StartTimestamp: start, EndTimestamp: end, Limit: limit}
	afterTimeRaw := strings.TrimSpace(c.Query("after_created_at"))
	afterID := strings.TrimSpace(c.Query("after_id"))
	if (afterTimeRaw == "") != (afterID == "") {
		return service.UnaBillingLogQuery{}, service.ErrUnaBillingLogsInvalid
	}
	if afterTimeRaw != "" {
		afterTime, err := strconv.ParseInt(afterTimeRaw, 10, 64)
		if err != nil {
			return service.UnaBillingLogQuery{}, service.ErrUnaBillingLogsInvalid
		}
		result.AfterCreatedAt = &afterTime
		result.AfterID = afterID
	}
	return result, nil
}

func decodeUnaInternalJSON(c *gin.Context, target any) error {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type must be application/json")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, unaInternalMaxJSONBody)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing json")
	}
	return nil
}
