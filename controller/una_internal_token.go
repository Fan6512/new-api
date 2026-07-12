package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func UnaInternalCreateToken(c *gin.Context) {
	var req dto.UnaTokenProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	provisioner, err := service.NewUnaTokenProvisionerFromEnv()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "una token provision is not configured"})
		return
	}
	response, err := provisioner.Create(c.Request.Context(), strings.TrimSpace(c.GetHeader("Idempotency-Key")), req)
	if err != nil {
		writeUnaProvisionError(c, err)
		return
	}
	status := http.StatusCreated
	if response.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, response)
}

func UnaInternalDisableToken(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid token id"})
		return
	}
	provisioner, err := service.NewUnaTokenProvisionerFromEnv()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "una token provision is not configured"})
		return
	}
	response, err := provisioner.Disable(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token disable failed"})
		return
	}
	c.JSON(http.StatusOK, response)
}

func writeUnaProvisionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrUnaProvisionInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
	case errors.Is(err, service.ErrUnaProvisionConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "idempotency key conflict"})
	case errors.Is(err, service.ErrUnaRecoveryExpired):
		c.JSON(http.StatusGone, gin.H{"error": "idempotency recovery expired"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token provision failed"})
	}
}
