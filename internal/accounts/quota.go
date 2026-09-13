package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	DefaultAccountsURL     = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"
	DefaultSubscriptionURL = "https://chatgpt.com/backend-api/subscriptions"
)

type QuotaWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type RateLimit struct {
	Allowed         bool         `json:"allowed"`
	LimitReached    bool         `json:"limit_reached"`
	PrimaryWindow   *QuotaWindow `json:"primary_window,omitempty"`
	SecondaryWindow *QuotaWindow `json:"secondary_window,omitempty"`
}

type QuotaSnapshot struct {
	UpstreamAccountID     string     `json:"upstream_account_id,omitempty"`
	Email                 string     `json:"email,omitempty"`
	PlanType              string     `json:"plan_type,omitempty"`
	SubscriptionExpiresAt *time.Time `json:"subscription_expires_at,omitempty"`
	RateLimit             *RateLimit `json:"rate_limit,omitempty"`
	FetchedAt             time.Time  `json:"fetched_at"`
}

type QuotaClient struct {
	UsageURL        string
	AccountsURL     string
	SubscriptionURL string
}

type QuotaCredentials struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
}

func NewQuotaClient() *QuotaClient {
	return &QuotaClient{UsageURL: DefaultUsageURL, AccountsURL: DefaultAccountsURL, SubscriptionURL: DefaultSubscriptionURL}
}

// Fetch reads the official Codex limit windows first. Account and subscription
// metadata are best-effort because those endpoints are not available for every
// plan, while /wham/usage remains the source of truth for quota percentages.
func (q *QuotaClient) Fetch(ctx context.Context, client *http.Client, creds QuotaCredentials) (*QuotaSnapshot, error) {
	if client == nil || strings.TrimSpace(creds.AccessToken) == "" {
		return nil, errors.New("账号访问令牌不可用")
	}
	if strings.TrimSpace(creds.AccountID) == "" {
		return nil, errors.New("账号缺少上游账号标识，请重新授权")
	}
	var usage struct {
		AccountID string     `json:"account_id"`
		Email     string     `json:"email"`
		PlanType  string     `json:"plan_type"`
		RateLimit *RateLimit `json:"rate_limit"`
	}
	if err := q.getJSON(ctx, client, q.UsageURL, creds, &usage); err != nil {
		return nil, err
	}
	snapshot := &QuotaSnapshot{
		UpstreamAccountID: firstNonEmpty(usage.AccountID, creds.AccountID),
		Email:             usage.Email,
		PlanType:          usage.PlanType,
		RateLimit:         usage.RateLimit,
		FetchedAt:         time.Now().UTC(),
	}
	if info, err := q.fetchAccountInfo(ctx, client, creds); err == nil && info != nil {
		snapshot.UpstreamAccountID = firstNonEmpty(info.AccountID, snapshot.UpstreamAccountID)
		snapshot.Email = firstNonEmpty(info.Email, snapshot.Email)
		snapshot.PlanType = firstNonEmpty(snapshot.PlanType, info.PlanType)
		snapshot.SubscriptionExpiresAt = info.ExpiresAt
	}
	if snapshot.SubscriptionExpiresAt == nil {
		if expiresAt, planType, err := q.fetchSubscription(ctx, client, creds, snapshot.UpstreamAccountID); err == nil {
			snapshot.SubscriptionExpiresAt = expiresAt
			snapshot.PlanType = firstNonEmpty(snapshot.PlanType, planType)
		}
	}
	return snapshot, nil
}

func (q *QuotaClient) getJSON(ctx context.Context, client *http.Client, endpoint string, creds QuotaCredentials, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("chatgpt-account-id", creds.AccountID)
	req.Header.Set("openai-beta", "codex-1")
	req.Header.Set("oai-language", "zh-CN")
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("sec-fetch-site", "none")
	req.Header.Set("sec-fetch-mode", "no-cors")
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("priority", "u=4, i")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连接官方账号服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("官方账号服务返回状态码 %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(dst); err != nil {
		return errors.New("官方账号服务返回了无法识别的数据")
	}
	return nil
}

type accountInfo struct {
	AccountID string
	Email     string
	PlanType  string
	ExpiresAt *time.Time
}

func (q *QuotaClient) fetchAccountInfo(ctx context.Context, client *http.Client, creds QuotaCredentials) (*accountInfo, error) {
	var payload struct {
		Accounts map[string]struct {
			Account struct {
				AccountID string `json:"account_id"`
				PlanType  string `json:"plan_type"`
				Email     string `json:"email"`
				IsDefault bool   `json:"is_default"`
			} `json:"account"`
			Entitlement struct {
				SubscriptionPlan string `json:"subscription_plan"`
				ExpiresAt        string `json:"expires_at"`
			} `json:"entitlement"`
		} `json:"accounts"`
	}
	if err := q.getJSON(ctx, client, q.AccountsURL, creds, &payload); err != nil {
		return nil, err
	}
	var selectedKey string
	for key, candidate := range payload.Accounts {
		id := firstNonEmpty(candidate.Account.AccountID, key)
		if id == creds.AccountID {
			selectedKey = key
			break
		}
		if selectedKey == "" || candidate.Account.IsDefault {
			selectedKey = key
		}
	}
	if selectedKey == "" {
		return nil, errors.New("账号信息响应中没有可用账号")
	}
	selected := payload.Accounts[selectedKey]
	info := &accountInfo{
		AccountID: firstNonEmpty(selected.Account.AccountID, selectedKey),
		Email:     selected.Account.Email,
		PlanType:  firstNonEmpty(selected.Account.PlanType, selected.Entitlement.SubscriptionPlan),
	}
	info.ExpiresAt = parseRFC3339(selected.Entitlement.ExpiresAt)
	return info, nil
}

func (q *QuotaClient) fetchSubscription(ctx context.Context, client *http.Client, creds QuotaCredentials, accountID string) (*time.Time, string, error) {
	u, err := url.Parse(q.SubscriptionURL)
	if err != nil {
		return nil, "", err
	}
	query := u.Query()
	query.Set("account_id", accountID)
	u.RawQuery = query.Encode()
	var payload struct {
		PlanType    string `json:"plan_type"`
		ActiveUntil string `json:"active_until"`
	}
	if err := q.getJSON(ctx, client, u.String(), creds, &payload); err != nil {
		return nil, "", err
	}
	return parseRFC3339(payload.ActiveUntil), payload.PlanType, nil
}

func parseRFC3339(value string) *time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
