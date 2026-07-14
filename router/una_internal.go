package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func registerUnaInternalRoutes(apiRouter *gin.RouterGroup) {
	route := apiRouter.Group("/una/internal")
	route.Use(middleware.UnaInternalAuth())
	{
		route.POST("/tokens", controller.UnaInternalCreateToken)
		route.POST("/tokens/recover", controller.UnaInternalRecoverToken)
		route.POST("/tokens/:id/disable", controller.UnaInternalDisableToken)
	}
}
