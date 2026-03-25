package fakes

import (
	"github.com/gin-gonic/gin"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

// newRouter creates a gin router with recovery middleware and a request
// recorder attached. All three fake servers share this setup.
func newRouter() (*gin.Engine, *recorder.RequestRecorder) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	rec := recorder.NewRecorder()
	r.Use(rec.GinMiddleware())
	return r, rec
}
