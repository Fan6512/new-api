package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUnaInternalAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "0123456789abcdef0123456789abcdef"

	tests := []struct {
		name       string
		configured string
		headers    []string
		wantStatus int
	}{
		{name: "not configured", wantStatus: http.StatusUnauthorized},
		{name: "configured secret too short", configured: "too-short", headers: []string{"Bearer too-short"}, wantStatus: http.StatusUnauthorized},
		{name: "missing authorization", configured: secret, wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", configured: secret, headers: []string{"Basic " + secret}, wantStatus: http.StatusUnauthorized},
		{name: "wrong credential", configured: secret, headers: []string{"Bearer 0123456789abcdef0123456789abcdeg"}, wantStatus: http.StatusUnauthorized},
		{name: "comma merged credential", configured: secret, headers: []string{"Bearer " + secret + ",Bearer " + secret}, wantStatus: http.StatusUnauthorized},
		{name: "duplicate authorization", configured: secret, headers: []string{"Bearer " + secret, "Bearer " + secret}, wantStatus: http.StatusUnauthorized},
		{name: "valid credential", configured: secret, headers: []string{"Bearer " + secret}, wantStatus: http.StatusNoContent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UNA_INTERNAL_PROVISION_TOKEN", tc.configured)
			router := gin.New()
			router.GET("/", UnaInternalAuth(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, value := range tc.headers {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tc.wantStatus, response.Body.String())
			}
			if response.Code == http.StatusUnauthorized && response.Body.String() != "{\"error\":\"una internal authorization failed\"}" {
				t.Fatalf("unexpected authorization error body: %s", response.Body.String())
			}
			if response.Body.String() != "" && containsSecret(response.Body.String(), secret) {
				t.Fatal("authorization response leaked configured credential")
			}
		})
	}
}

func containsSecret(value, secret string) bool {
	return len(secret) > 0 && len(value) >= len(secret) && stringContains(value, secret)
}

func stringContains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
