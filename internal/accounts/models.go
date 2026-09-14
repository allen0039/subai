package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// DefaultCodexModelsURL is the model manifest used by the official Codex
// client.  Unlike a general pricing catalog, it represents models actually
// advertised to a Codex OAuth account.
const DefaultCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

type ModelClient struct {
	CodexModelsURL string
}

func NewModelClient() *ModelClient { return &ModelClient{CodexModelsURL: DefaultCodexModelsURL} }

// FetchCodexModels reads the official Codex model manifest. Only GPT models
// are exposed by this product's Codex account pool; pricing-only and other
// provider models must never become selectable merely because they have a
// price entry.
func (c *ModelClient) FetchCodexModels(ctx context.Context, client *http.Client, creds QuotaCredentials) ([]string, error) {
	if client == nil || strings.TrimSpace(creds.AccessToken) == "" || strings.TrimSpace(creds.AccountID) == "" {
		return nil, errors.New("账号凭证不可用，请重新授权")
	}
	endpoint := strings.TrimSpace(c.CodexModelsURL)
	if endpoint == "" {
		return nil, errors.New("Codex 模型服务尚未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	applyCodexRequestHeaders(req, creds)
	// The manifest requires version negotiation independently of OAuth.
	const clientVersion = "0.146.0"
	query := req.URL.Query()
	query.Set("client_version", clientVersion)
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Version", clientVersion)
	req.Header.Set("Originator", "codex-tui")
	req.Header.Set("User-Agent", "codex-tui/"+clientVersion+" (Ubuntu 22.4.0; x86_64) xterm-256color")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("官方模型服务请求失败（状态码 %d）", resp.StatusCode)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("官方模型列表格式异常")
	}
	// The Codex manifest uses "models". Accepting the OpenAI-compatible
	// "data" representation as well keeps the account import resilient to a
	// compatible upstream proxy, while model filtering below remains strict.
	rawModels := envelope["models"]
	if len(rawModels) == 0 {
		rawModels = envelope["data"]
	}
	if len(rawModels) == 0 {
		return nil, errors.New("官方模型服务未返回模型列表")
	}
	var models []json.RawMessage
	if err := json.Unmarshal(rawModels, &models); err != nil {
		return nil, errors.New("官方模型列表格式异常")
	}
	seen := map[string]bool{}
	for _, raw := range models {
		var item struct {
			Slug  string `json:"slug"`
			ID    string `json:"id"`
			Model string `json:"model"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		name := strings.TrimSpace(item.Slug)
		if name == "" {
			name = strings.TrimSpace(item.ID)
		}
		if name == "" {
			name = strings.TrimSpace(item.Model)
		}
		if strings.HasPrefix(strings.ToLower(name), "gpt-") {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for model := range seen {
		out = append(out, model)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, errors.New("官方模型服务未返回可用的 GPT 模型")
	}
	return out, nil
}
