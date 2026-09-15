package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthRoutesExposeOperationsButNotSub2APILoginImports(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{
		OpenAIOAuth: &adminhandler.OpenAIOAuthHandler{},
	}}
	registerOpenAIOAuthRoutes(router.Group("/api/v1/admin"), handlers)

	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}

	for _, removed := range []string{
		"POST /api/v1/admin/openai/generate-auth-url",
		"POST /api/v1/admin/openai/exchange-code",
		"POST /api/v1/admin/openai/refresh-token",
		"POST /api/v1/admin/openai/create-from-oauth",
		"POST /api/v1/admin/openai/create-from-codex-pat",
	} {
		require.False(t, routes[removed], removed)
	}

	for _, retained := range []string{
		"POST /api/v1/admin/openai/accounts/:id/refresh",
		"GET /api/v1/admin/openai/accounts/:id/quota",
		"POST /api/v1/admin/openai/accounts/:id/quota/refresh",
		"POST /api/v1/admin/openai/accounts/:id/reset-quota",
	} {
		require.True(t, routes[retained], retained)
	}
}
