package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func setupUnaInternalControllerTest(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Token{}, &model.UnaTokenProvision{}); err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	machineToken := "0123456789abcdef0123456789abcdef"
	t.Setenv("UNA_INTERNAL_PROVISION_TOKEN", machineToken)
	t.Setenv("UNA_TOKEN_RECOVERY_KEK_B64", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)))
	t.Setenv("UNA_TOKEN_RECOVERY_KEK_VERSION", "test-v1")
	t.Setenv("UNA_TOKEN_RECOVERY_TTL", "5m")

	router := gin.New()
	route := router.Group("/api/una/internal")
	route.Use(middleware.UnaInternalAuth())
	route.POST("/tokens", UnaInternalCreateToken)
	route.POST("/tokens/:id/disable", UnaInternalDisableToken)
	return router, db, machineToken
}

func performUnaInternalRequest(router http.Handler, machineToken, idempotencyKey, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/una/internal/tokens", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+machineToken)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestUnaInternalCreateTokenHTTPContract(t *testing.T) {
	router, db, machineToken := setupUnaInternalControllerTest(t)
	body := `{"owner_user_id":7,"name":"Una key","quota":123,"status":1}`

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/una/internal/tokens", strings.NewReader(body))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedResponse := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorizedResponse.Code)
	}

	first := performUnaInternalRequest(router, machineToken, "http-idem", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", first.Code, first.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	rawKey, _ := created["raw_key"].(string)
	if !strings.HasPrefix(rawKey, "sk-una-") || created["replayed"] != false {
		t.Fatalf("unexpected create payload: %v", created)
	}

	replay := performUnaInternalRequest(router, machineToken, "http-idem", body)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"replayed":true`) || !strings.Contains(replay.Body.String(), rawKey) {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}

	conflict := performUnaInternalRequest(router, machineToken, "http-idem", `{"owner_user_id":7,"name":"different","quota":123,"status":1}`)
	if conflict.Code != http.StatusConflict || strings.Contains(conflict.Body.String(), rawKey) {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	if err := db.Model(&model.UnaTokenProvision{}).Where("idempotency_key = ?", "http-idem").Update("recoverable_until", 1).Error; err != nil {
		t.Fatal(err)
	}
	expired := performUnaInternalRequest(router, machineToken, "http-idem", body)
	if expired.Code != http.StatusGone || strings.Contains(expired.Body.String(), rawKey) {
		t.Fatalf("expired status=%d body=%s", expired.Code, expired.Body.String())
	}
}

func TestUnaInternalDisableTokenHTTPContract(t *testing.T) {
	router, db, machineToken := setupUnaInternalControllerTest(t)
	created := performUnaInternalRequest(router, machineToken, "http-disable", `{"owner_user_id":7,"name":"Una key","quota":123,"status":1}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var provision model.UnaTokenProvision
	if err := db.First(&provision).Error; err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/una/internal/tokens/"+strconv.Itoa(provision.TokenId)+"/disable", nil)
	request.Header.Set("Authorization", "Bearer "+machineToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"disabled"`) {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}

	missing := httptest.NewRequest(http.MethodPost, "/api/una/internal/tokens/999999/disable", nil)
	missing.Header.Set("Authorization", "Bearer "+machineToken)
	missingResponse := httptest.NewRecorder()
	router.ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missingResponse.Code, missingResponse.Body.String())
	}
}
