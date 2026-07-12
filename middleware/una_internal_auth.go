package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

func UnaInternalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := strings.TrimSpace(os.Getenv("UNA_INTERNAL_PROVISION_TOKEN"))
		values := c.Request.Header.Values("Authorization")
		if len(expected) < 32 || len(values) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "una internal authorization failed"})
			return
		}
		value := strings.TrimSpace(values[0])
		if !strings.HasPrefix(value, "Bearer ") || strings.Contains(value, ",") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "una internal authorization failed"})
			return
		}
		actual := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
		if len(actual) != len(expected) || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "una internal authorization failed"})
			return
		}
		c.Next()
	}
}
