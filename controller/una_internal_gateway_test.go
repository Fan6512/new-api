package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupUnaGatewayControllerTest(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.UnaChannelMirror{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	model.LOG_DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.RedisEnabled = previousRedisEnabled
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	machineToken := "0123456789abcdef0123456789abcdef"
	t.Setenv("UNA_INTERNAL_PROVISION_TOKEN", machineToken)
	router := gin.New()
	route := router.Group("/api/una/internal")
	route.Use(middleware.UnaInternalAuth())
	route.PUT("/channels", UnaInternalApplyChannel)
	route.GET("/logs", UnaInternalListBillingLogs)
	return router, db, machineToken
}

func performUnaGatewayRequest(router http.Handler, method, path, machineToken, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+machineToken)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestUnaInternalChannelMirrorHTTPContract(t *testing.T) {
	router, db, machineToken := setupUnaGatewayControllerTest(t)
	weight := uint(1)
	priority := int64(0)
	mapping := `{}`
	channel := model.Channel{
		Type: 1, Key: "upstream-secret", Status: common.ChannelStatusManuallyDisabled,
		Name: "preconfigured", Weight: &weight, Models: "legacy-model", Group: "default",
		ModelMapping: &mapping, Priority: &priority,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"contract_version":"una-channel-mirror.v1","mapping_id":"map-1","desired_version":1,"channel_id":%d,"model":"deepseek-v3","channel_name":"cmcc-primary","upstream_model":"DeepSeek-V3.1","priority":10,"weight":2,"cost_ratio":"1.0000","enabled":true}`, channel.Id)

	unauthorized := performUnaGatewayRequest(router, http.MethodPut, "/api/una/internal/channels", "", body)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	first := performUnaGatewayRequest(router, http.MethodPut, "/api/una/internal/channels", machineToken, body)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"contract_version":"una-channel-mirror.v1"`) {
		t.Fatalf("apply status=%d body=%s", first.Code, first.Body.String())
	}
	var stored model.Channel
	if err := db.First(&stored, channel.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Key != "upstream-secret" || !strings.Contains(stored.Models, "deepseek-v3") || !strings.Contains(stored.GetModelMapping(), `"deepseek-v3":"DeepSeek-V3.1"`) {
		t.Fatalf("unexpected mirrored channel: %+v", stored)
	}
	var ability model.Ability
	if err := db.Where("channel_id = ? AND model = ?", channel.Id, "deepseek-v3").First(&ability).Error; err != nil {
		t.Fatal(err)
	}
	if ability.Priority == nil || *ability.Priority != 10 || ability.Weight != 2 || !ability.Enabled {
		t.Fatalf("unexpected mirrored ability: %+v", ability)
	}

	replay := performUnaGatewayRequest(router, http.MethodPut, "/api/una/internal/channels", machineToken, body)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	conflictBody := strings.Replace(body, `"weight":2`, `"weight":3`, 1)
	conflict := performUnaGatewayRequest(router, http.MethodPut, "/api/una/internal/channels", machineToken, conflictBody)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestUnaInternalBillingLogsHTTPContract(t *testing.T) {
	router, db, machineToken := setupUnaGatewayControllerTest(t)
	weight := uint(1)
	channel := model.Channel{Type: 1, Key: "secret", Status: common.ChannelStatusEnabled, Name: "cmcc-primary", Weight: &weight, Models: "DeepSeek-V3.1", Group: "default"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	createdAt := int64(1784419200)
	logs := []model.Log{
		{Id: 1, UserId: 7, CreatedAt: createdAt, Type: model.LogTypeConsume, Content: "private prompt", TokenName: "secret-name", ModelName: "deepseek-v3", Quota: 12, PromptTokens: 10, CompletionTokens: 2, UseTime: 1, IsStream: true, ChannelId: channel.Id, TokenId: 9, Other: `{"request_path":"/v1/chat/completions","cache_tokens":3,"upstream_model_name":"DeepSeek-V3.1"}`},
		{Id: 2, UserId: 7, CreatedAt: createdAt, Type: model.LogTypeConsume, Content: "private prompt 2", TokenName: "secret-name", ModelName: "deepseek-v3", Quota: 7, PromptTokens: 5, CompletionTokens: 2, UseTime: 2, ChannelId: channel.Id, TokenId: 9, Other: `{"request_path":"/v1/chat/completions","upstream_model_name":"DeepSeek-V3.1"}`},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}

	path := fmt.Sprintf("/api/una/internal/logs?type=2&start_timestamp=%d&end_timestamp=%d&limit=1", createdAt, createdAt)
	first := performUnaGatewayRequest(router, http.MethodGet, path, machineToken, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), "private prompt") || strings.Contains(first.Body.String(), "secret-name") || !strings.Contains(first.Body.String(), `"has_more":true`) {
		t.Fatalf("response leaked data or missed cursor: %s", first.Body.String())
	}
	var decoded struct {
		NextCursor struct {
			CreatedAt int64  `json:"created_at"`
			ID        string `json:"id"`
		} `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	secondPath := fmt.Sprintf("%s&after_created_at=%d&after_id=%s", path, decoded.NextCursor.CreatedAt, decoded.NextCursor.ID)
	second := performUnaGatewayRequest(router, http.MethodGet, secondPath, machineToken, "")
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"id":"00000000000000000002"`) || !strings.Contains(second.Body.String(), `"has_more":false`) {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	invalid := performUnaGatewayRequest(router, http.MethodGet, strings.Replace(path, "type=2", "type=5", 1), machineToken, "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("type gate status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}
