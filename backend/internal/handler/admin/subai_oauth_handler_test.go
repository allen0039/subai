package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type subaiHandlerOAuthClient struct{ exchanges int }

func (s *subaiHandlerOAuthClient) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	s.exchanges++
	return &openai.TokenResponse{AccessToken: "private-access", RefreshToken: "private-refresh", ExpiresIn: 3600}, nil
}
func (s *subaiHandlerOAuthClient) RefreshToken(context.Context, string, string) (*openai.TokenResponse, error) {
	panic("unexpected refresh")
}
func (s *subaiHandlerOAuthClient) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	panic("unexpected refresh")
}

func TestSubAILegacyOAuthEndpointsPersistWithoutExposingTokens(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "reauthorize"}[reuse], func(t *testing.T) {
			client := &subaiHandlerOAuthClient{}
			svc := service.NewOpenAIOAuthService(nil, client)
			defer svc.Stop()
			adminSvc := newStubAdminService()
			adminSvc.getAccountResult = &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: "disabled"}
			h := NewOpenAIOAuthHandler(svc, adminSvc, nil, nil, nil)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if c.GetHeader("test-owner") != "" {
					id := int64(7)
					if c.GetHeader("test-owner") == "other" {
						id = 8
					}
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: id})
				}
			})
			router.POST("/sessions", h.StartSubAIOAuth)
			router.GET("/sessions/:sessionID", h.GetSubAIOAuth)
			router.POST("/sessions/:sessionID/callback", h.CompleteSubAIOAuth)
			router.GET("/api/oauth/callback", h.SubAIPublicCallback)
			request := func(method, path, body, owner string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("test-owner", owner)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				return w
			}
			require.Equal(t, http.StatusUnauthorized, request("POST", "/sessions", "{}", "").Code)
			body := "{}"
			if reuse {
				body = `{"reuse_account_id":"42"}`
			}
			start := request("POST", "/sessions", body, "self")
			require.Equal(t, http.StatusCreated, start.Code, start.Body.String())
			var result struct {
				ID    string `json:"id"`
				State string `json:"state"`
				URL   string `json:"authorize_url"`
			}
			require.NoError(t, json.Unmarshal(start.Body.Bytes(), &result))
			require.NotEmpty(t, result.ID)
			require.Equal(t, http.StatusNotFound, request("GET", "/sessions/"+result.ID, "", "other").Code)
			auth, err := url.Parse(result.URL)
			require.NoError(t, err)
			callback := auth.Query().Get("redirect_uri") + "?code=code&state=" + result.State
			payload, err := json.Marshal(map[string]string{"callback_url": callback})
			require.NoError(t, err)
			path := "/sessions/" + result.ID + "/callback"
			require.Equal(t, http.StatusNotFound, request("POST", path, string(payload), "other").Code)
			var complete *httptest.ResponseRecorder
			if reuse {
				complete = request("POST", path, string(payload), "self")
			} else {
				complete = request("GET", "/api/oauth/callback?code=code&state="+url.QueryEscape(result.State), "", "")
			}
			require.Equal(t, http.StatusOK, complete.Code, complete.Body.String())
			require.NotContains(t, complete.Body.String(), "private-")
			require.Equal(t, http.StatusConflict, request("POST", path, string(payload), "self").Code)
			require.Equal(t, 1, client.exchanges)
			status := request("GET", "/sessions/"+result.ID, "", "self")
			require.Equal(t, http.StatusOK, status.Code)
			require.Contains(t, status.Body.String(), "completed")
			require.NotContains(t, status.Body.String(), "private-")
			if reuse {
				require.Equal(t, 1, adminSvc.updateAccountCalls)
				require.Equal(t, "", adminSvc.lastUpdateAccountInput.Status)
				require.Empty(t, adminSvc.createdAccounts)
			} else {
				require.Len(t, adminSvc.createdAccounts, 1)
				require.Equal(t, service.PlatformOpenAI, adminSvc.createdAccounts[0].Platform)
				require.Equal(t, "private-access", adminSvc.createdAccounts[0].Credentials["access_token"])
			}
		})
	}
}
