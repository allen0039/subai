package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// StartSubAIOAuth preserves the original SubAI API. Platform login/permissions
// belong to sub2api; this endpoint only authenticates an upstream GPT account.
func (h *OpenAIOAuthHandler) StartSubAIOAuth(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Administrator authentication required")
		return
	}
	var req struct {
		ReuseAccountID string `json:"reuse_account_id"`
		ProxyID        *int64 `json:"proxy_id"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 20*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid authorization request")
		return
	}
	var accountID int64
	if req.ReuseAccountID != "" {
		var err error
		accountID, err = strconv.ParseInt(req.ReuseAccountID, 10, 64)
		if err != nil || accountID <= 0 {
			response.BadRequest(c, "reuse_account_id must be a sub2api account ID")
			return
		}
		account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if account.Platform != service.PlatformOpenAI || !account.IsOAuth() || account.IsCredentialShadow() {
			response.BadRequest(c, "account is not a GPT OAuth account")
			return
		}
		req.ProxyID = account.ProxyID
	}
	result, err := h.openaiOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, "", service.PlatformOpenAI, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.openaiOAuthService.BindSubAISession(result.SessionID, subject.UserID, accountID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	parsed, _ := url.Parse(result.AuthURL)
	c.JSON(http.StatusCreated, gin.H{"id": result.SessionID, "state": parsed.Query().Get("state"), "authorize_url": result.AuthURL})
}

func (h *OpenAIOAuthHandler) GetSubAIOAuth(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Administrator authentication required")
		return
	}
	session, err := h.openaiOAuthService.GetSubAISession(c.Param("sessionID"), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *OpenAIOAuthHandler) CompleteSubAIOAuth(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Administrator authentication required")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 20*1024)
	var req struct {
		CallbackURL string `json:"callback_url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "complete callback_url is required")
		return
	}
	h.completeSubAIOAuth(c, c.Param("sessionID"), subject.UserID, req.CallbackURL)
}

// Public provider callbacks are authenticated by the random, expiring OAuth
// state. They share the exact single-use completion path with manual submission.
func (h *OpenAIOAuthHandler) SubAIPublicCallback(c *gin.Context) {
	q, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("state") == "" || q.Get("code") == "" || q.Get("error") != "" {
		response.BadRequest(c, "state and code are required")
		return
	}
	id, ownerID, redirect, err := h.openaiOAuthService.ResolveSubAICallback(q.Get("state"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	target, err := url.Parse(redirect)
	if err != nil {
		response.BadRequest(c, "invalid OAuth redirect configuration")
		return
	}
	target.RawQuery = url.Values{"code": {q.Get("code")}, "state": {q.Get("state")}}.Encode()
	h.completeSubAIOAuth(c, id, ownerID, target.String())
}

func (h *OpenAIOAuthHandler) completeSubAIOAuth(c *gin.Context, id string, ownerID int64, callbackURL string) {
	session, err := h.openaiOAuthService.GetSubAISession(id, ownerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if session.Status != "pending" {
		response.Error(c, http.StatusConflict, "authorization is no longer pending")
		return
	}
	// Check target before spending the single-use authorization code.
	if session.AccountID != 0 {
		account, err := h.adminService.GetAccount(c.Request.Context(), session.AccountID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if account.Platform != service.PlatformOpenAI || !account.IsOAuth() || account.IsCredentialShadow() {
			response.BadRequest(c, "account is not a GPT OAuth account")
			return
		}
	}
	token, err := h.openaiOAuthService.ExchangeCode(c.Request.Context(), &service.OpenAIExchangeCodeInput{SessionID: id, OwnerID: ownerID, CallbackURL: strings.TrimSpace(callbackURL)})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Subsequent attempts must not create a second account, including a lost HTTP response.
	if err := h.openaiOAuthService.RecordSubAIStatus(id, ownerID, session.AccountID, "saving"); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	credentials := h.openaiOAuthService.BuildAccountCredentials(token)
	var account *service.Account
	if session.AccountID == 0 {
		name := token.Email
		if name == "" {
			name = "codex-" + id[:8]
		}
		account, err = h.adminService.CreateAccount(c.Request.Context(), &service.CreateAccountInput{Name: name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: credentials, ProxyID: session.ProxyID, Concurrency: 1, Priority: 50})
	} else {
		account, err = h.adminService.UpdateAccount(c.Request.Context(), session.AccountID, &service.UpdateAccountInput{Credentials: credentials})
	}
	if err != nil {
		_ = h.openaiOAuthService.RecordSubAIStatus(id, ownerID, session.AccountID, "failed")
		response.ErrorFrom(c, err)
		return
	}
	if session.AccountID != 0 && h.rateLimitService != nil {
		_, _ = h.rateLimitService.RecoverAccountState(c.Request.Context(), account.ID, service.AccountRecoveryOptions{})
	}
	if h.tokenCacheInvalidator != nil {
		_ = h.tokenCacheInvalidator.InvalidateToken(c.Request.Context(), account)
	}
	if err := h.openaiOAuthService.RecordSubAIStatus(id, ownerID, account.ID, "completed"); err != nil {
		// The account already exists. Report success so a status-store outage cannot
		// encourage the operator to repeat an otherwise successful account mutation.
		c.JSON(http.StatusOK, gin.H{"ok": true, "account_id": account.ID, "quota_synced": false, "status_saved": false})
		return
	}
	quotaSynced := false
	if h.quotaService != nil {
		_, err := h.quotaService.QueryUsage(c.Request.Context(), account.ID)
		quotaSynced = err == nil
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "account_id": account.ID, "quota_synced": quotaSynced})
}
