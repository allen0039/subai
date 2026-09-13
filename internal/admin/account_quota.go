package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"subai/internal/accounts"
	"subai/internal/egress"
	"subai/internal/storage"
)

func (s *Server) refreshAccountQuota(w http.ResponseWriter, r *http.Request, accountID string) {
	if s.Quota == nil || s.Egress == nil {
		s.writeErr(w, http.StatusServiceUnavailable, "额度同步服务尚未配置")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	snapshot, err := s.syncAccountQuota(ctx, accountID)
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, quotaErrorMessage(err))
		return
	}
	s.DB.LogAdminEvent(ctx, actorFrom(r), "account.quota_sync", "account", accountID,
		storage.SanitizeForAdminEvent(map[string]any{"plan_type": snapshot.PlanType}), "")
	s.writeJSON(w, 200, map[string]any{"quota": snapshot, "quota_fetched_at": snapshot.FetchedAt, "quota_error": ""})
}

func (s *Server) syncAccountQuota(ctx context.Context, accountID string) (*accounts.QuotaSnapshot, error) {
	creds, policyID, expiresAt, err := s.loadQuotaAccount(ctx, accountID)
	if err == nil && expiresAt != nil && time.Until(*expiresAt) < 5*time.Minute && s.OAuth != nil {
		if refreshErr := s.OAuth.Refresh(ctx, accountID); refreshErr != nil && time.Now().After(*expiresAt) {
			err = errors.New("账号凭证已到期，自动刷新失败，请重新授权")
		} else {
			creds, policyID, _, err = s.loadQuotaAccount(ctx, accountID)
		}
	}
	var snapshot *accounts.QuotaSnapshot
	if err == nil {
		snapshot, err = s.fetchQuotaViaPolicy(ctx, policyID, creds)
	}
	if err != nil {
		message := quotaErrorMessage(err)
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, _ = s.DB.Pool.Exec(persistCtx, `
			INSERT INTO account_quota_snapshots(account_id,last_attempt_at,fetch_error,updated_at)
			VALUES($1,now(),$2,now())
			ON CONFLICT(account_id) DO UPDATE SET last_attempt_at=now(),fetch_error=EXCLUDED.fetch_error,updated_at=now()`, accountID, message)
		return nil, errors.New(message)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, errors.New("保存额度数据失败")
	}
	_, err = s.DB.Pool.Exec(ctx, `
		INSERT INTO account_quota_snapshots(account_id,snapshot,fetched_at,last_attempt_at,fetch_error,updated_at)
		VALUES($1,$2,$3,now(),'',now())
		ON CONFLICT(account_id) DO UPDATE SET snapshot=EXCLUDED.snapshot,fetched_at=EXCLUDED.fetched_at,
			last_attempt_at=now(),fetch_error='',updated_at=now()`, accountID, raw, snapshot.FetchedAt)
	if err != nil {
		return nil, errors.New("保存额度数据失败")
	}
	return snapshot, nil
}

func (s *Server) loadQuotaAccount(ctx context.Context, accountID string) (accounts.QuotaCredentials, string, *time.Time, error) {
	var sealed []byte
	var policyID string
	var expiresAt *time.Time
	if err := s.DB.Pool.QueryRow(ctx, `SELECT credentials_ciphertext,COALESCE(egress_policy_id::text,''),expires_at FROM accounts WHERE id=$1`, accountID).Scan(&sealed, &policyID, &expiresAt); err != nil {
		return accounts.QuotaCredentials{}, "", nil, errors.New("账号不存在")
	}
	plain, err := s.DB.Decrypt(sealed)
	if err != nil {
		return accounts.QuotaCredentials{}, "", nil, errors.New("读取账号凭证失败")
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	var creds accounts.QuotaCredentials
	if err := json.Unmarshal(plain, &creds); err != nil || creds.AccessToken == "" {
		return accounts.QuotaCredentials{}, "", nil, errors.New("账号凭证不可用")
	}
	return creds, policyID, expiresAt, nil
}

func (s *Server) fetchQuotaViaPolicy(ctx context.Context, policyID string, creds accounts.QuotaCredentials) (*accounts.QuotaSnapshot, error) {
	profiles := []egress.Profile{{ID: "direct", Name: "直接连接", Kind: "direct", Status: "active"}}
	if policyID != "" {
		policy, err := s.Egress.GetPolicy(ctx, policyID)
		if err != nil {
			return nil, errors.New("账号出口策略不可用")
		}
		profiles = append([]egress.Profile{policy.Primary}, policy.Fallbacks...)
	}
	var lastErr error
	for _, profile := range profiles {
		client, err := egress.ClientForProfile(profile, 25*time.Second)
		if err == nil {
			if snapshot, fetchErr := s.Quota.Fetch(ctx, client, creds); fetchErr == nil {
				return snapshot, nil
			} else {
				lastErr = fetchErr
			}
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

func quotaErrorMessage(err error) string {
	if err == nil {
		return "同步官方额度失败"
	}
	text := err.Error()
	if strings.Contains(text, "状态码 401") || strings.Contains(text, "状态码 403") {
		return "官方账号认证失败，请重新授权后再刷新"
	}
	if strings.Contains(text, "状态码 429") {
		return "官方账号服务暂时限制了查询，请稍后刷新"
	}
	if strings.Contains(text, "超时") || strings.Contains(text, "deadline") {
		return "同步官方额度超时，请检查账号代理后重试"
	}
	return text
}
