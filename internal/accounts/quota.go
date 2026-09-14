package accounts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	DefaultResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	DefaultResetConsumeURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
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

// ResetCredit intentionally excludes upstream card identifiers and tokens.
type ResetCredit struct {
	ExpiresAt string `json:"expires_at,omitempty"`
}

type ResetCredits struct {
	AvailableCount int           `json:"available_count"`
	Credits        []ResetCredit `json:"credits,omitempty"`
}

type QuotaSnapshot struct {
	UpstreamAccountID     string        `json:"upstream_account_id,omitempty"`
	Email                 string        `json:"email,omitempty"`
	PlanType              string        `json:"plan_type,omitempty"`
	SubscriptionExpiresAt *time.Time    `json:"subscription_expires_at,omitempty"`
	RateLimit             *RateLimit    `json:"rate_limit,omitempty"`
	RateLimitResetCredits *ResetCredits `json:"rate_limit_reset_credits,omitempty"`
	FetchedAt             time.Time     `json:"fetched_at"`
}

type QuotaClient struct {
	UsageURL        string
	ResetCreditsURL string
	ResetConsumeURL string
	AccountsURL     string
	SubscriptionURL string
}

type QuotaCredentials struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
}

func NewQuotaClient() *QuotaClient {
	return &QuotaClient{UsageURL: DefaultUsageURL, ResetCreditsURL: DefaultResetCreditsURL, ResetConsumeURL: DefaultResetConsumeURL, AccountsURL: DefaultAccountsURL, SubscriptionURL: DefaultSubscriptionURL}
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
		AccountID             string        `json:"account_id"`
		Email                 string        `json:"email"`
		PlanType              string        `json:"plan_type"`
		RateLimit             *RateLimit    `json:"rate_limit"`
		RateLimitResetCredits *ResetCredits `json:"rate_limit_reset_credits"`
	}
	if err := q.getJSON(ctx, client, q.UsageURL, creds, &usage); err != nil {
		return nil, err
	}
	snapshot := &QuotaSnapshot{
		UpstreamAccountID:     firstNonEmpty(usage.AccountID, creds.AccountID),
		Email:                 usage.Email,
		PlanType:              usage.PlanType,
		RateLimit:             usage.RateLimit,
		RateLimitResetCredits: usage.RateLimitResetCredits,
		FetchedAt:             time.Now().UTC(),
	}
	if details, err := q.fetchResetCredits(ctx, client, creds); err == nil && details != nil {
		if snapshot.RateLimitResetCredits == nil {
			snapshot.RateLimitResetCredits = &ResetCredits{}
		}
		if details.AvailableCount != nil {
			snapshot.RateLimitResetCredits.AvailableCount = *details.AvailableCount
		} else if details.CreditListPresent {
			snapshot.RateLimitResetCredits.AvailableCount = details.AvailableCreditCount
		}
		if details.CreditListPresent {
			snapshot.RateLimitResetCredits.Credits = details.Credits
		}
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

// ResetCreditResult contains only the safe confirmation metadata returned by
// the official consume operation. A card ID is deliberately never retained.
type ResetCreditResult struct {
	Code         string `json:"code,omitempty"`
	WindowsReset int    `json:"windows_reset,omitempty"`
}

// ConsumeResetCredit consumes one official reset card. Callers must not retry
// this method after a transport error: the upstream may have accepted the
// request even when its response was lost.
func (q *QuotaClient) ConsumeResetCredit(ctx context.Context, client *http.Client, creds QuotaCredentials) (*ResetCreditResult, error) {
	if client == nil || strings.TrimSpace(creds.AccessToken) == "" {
		return nil, errors.New("账号访问令牌不可用")
	}
	if strings.TrimSpace(creds.AccountID) == "" {
		return nil, errors.New("账号缺少上游账号标识，请重新授权")
	}
	if strings.TrimSpace(q.ResetConsumeURL) == "" {
		return nil, errors.New("重置卡服务尚未配置")
	}
	redeemRequestID, err := newRedeemRequestID()
	if err != nil {
		return nil, errors.New("生成重置卡请求标识失败")
	}
	var result ResetCreditResult
	if err := q.postJSON(ctx, client, q.ResetConsumeURL, creds, map[string]string{"redeem_request_id": redeemRequestID}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

type resetCreditDetails struct {
	AvailableCount       *int
	AvailableCreditCount int
	CreditListPresent    bool
	Credits              []ResetCredit
}

type resetCreditPayload struct {
	ExpiresAt      string `json:"expires_at"`
	ExpiresAtCamel string `json:"expiresAt"`
	ResetType      string `json:"reset_type"`
	ResetTypeCamel string `json:"resetType"`
	Status         string `json:"status"`
}

func (q *QuotaClient) fetchResetCredits(ctx context.Context, client *http.Client, creds QuotaCredentials) (*resetCreditDetails, error) {
	if strings.TrimSpace(q.ResetCreditsURL) == "" {
		return nil, nil
	}
	var raw json.RawMessage
	if err := q.getJSON(ctx, client, q.ResetCreditsURL, creds, &raw); err != nil {
		return nil, err
	}
	details, err := parseResetCreditDetails(raw)
	if err != nil {
		return nil, err
	}
	return &details, nil
}

func parseResetCreditDetails(body []byte) (resetCreditDetails, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return resetCreditDetails{}, nil
	}
	var count *int
	var creditsRaw json.RawMessage
	listPresent := false
	if trimmed[0] == '[' {
		creditsRaw = trimmed
		listPresent = true
	} else {
		var envelope struct {
			AvailableCount      *int             `json:"available_count"`
			AvailableCountCamel *int             `json:"availableCount"`
			Credits             *json.RawMessage `json:"credits"`
			ResetCredits        *json.RawMessage `json:"rate_limit_reset_credits"`
			Items               *json.RawMessage `json:"items"`
			Data                *json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return resetCreditDetails{}, err
		}
		count = envelope.AvailableCount
		if count == nil {
			count = envelope.AvailableCountCamel
		}
		for _, candidate := range []*json.RawMessage{envelope.Credits, envelope.ResetCredits, envelope.Items, envelope.Data} {
			if candidate != nil && len(bytes.TrimSpace(*candidate)) > 0 && !bytes.Equal(bytes.TrimSpace(*candidate), []byte("null")) {
				creditsRaw = *candidate
				listPresent = true
				break
			}
		}
	}
	var rawCredits []*resetCreditPayload
	if listPresent {
		if err := json.Unmarshal(creditsRaw, &rawCredits); err != nil {
			return resetCreditDetails{}, err
		}
	}
	result := resetCreditDetails{AvailableCount: count, CreditListPresent: listPresent}
	for _, raw := range rawCredits {
		if raw == nil {
			continue
		}
		resetType := firstNonEmpty(raw.ResetType, raw.ResetTypeCamel)
		if resetType != "" && !strings.EqualFold(resetType, "codex_rate_limits") {
			continue
		}
		if status := strings.TrimSpace(raw.Status); status != "" && !strings.EqualFold(status, "available") {
			continue
		}
		result.AvailableCreditCount++
		if expiresAt := firstNonEmpty(raw.ExpiresAt, raw.ExpiresAtCamel); expiresAt != "" {
			result.Credits = append(result.Credits, ResetCredit{ExpiresAt: expiresAt})
		}
	}
	return result, nil
}

func (q *QuotaClient) getJSON(ctx context.Context, client *http.Client, endpoint string, creds QuotaCredentials, dst any) error {
	return q.requestJSON(ctx, client, http.MethodGet, endpoint, creds, nil, dst)
}

func (q *QuotaClient) postJSON(ctx context.Context, client *http.Client, endpoint string, creds QuotaCredentials, body any, dst any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return q.requestJSON(ctx, client, http.MethodPost, endpoint, creds, bytes.NewReader(encoded), dst)
}

func (q *QuotaClient) requestJSON(ctx context.Context, client *http.Client, method, endpoint string, creds QuotaCredentials, body io.Reader, dst any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("chatgpt-account-id", creds.AccountID)
	req.Header.Set("openai-beta", "codex-1")
	req.Header.Set("oai-language", "zh-CN")
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
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

func newRedeemRequestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	v := hex.EncodeToString(b)
	return v[0:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], nil
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
