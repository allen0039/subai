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
	"sync"
	"time"
)

// DefaultCodexModelsURL is the model manifest used by the official Codex
// client.  Unlike a general pricing catalog, it represents models actually
// advertised to a Codex OAuth account.
const DefaultCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

type ModelClient struct {
	CodexModelsURL string
	versionMu      sync.Mutex
	version        string
	versionChecked time.Time
}

// Follow stable Codex releases, as Sub2API does: the manifest is negotiated
// by client version. Never attach account credentials to release discovery.
func (c *ModelClient) clientVersion(ctx context.Context) string {
	c.versionMu.Lock()
	defer c.versionMu.Unlock()
	if c.version == "" {
		c.version = "0.154.0"
	}
	if c.CodexModelsURL != DefaultCodexModelsURL || time.Since(c.versionChecked) < 6*time.Hour {
		return c.version
	}
	c.versionChecked = time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/openai/codex/releases/latest", nil)
	if err != nil {
		return c.version
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return c.version
	}
	defer resp.Body.Close()
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release) != nil || release.Draft || release.Prerelease {
		return c.version
	}
	var major, minor, patch int
	v := strings.TrimPrefix(release.Tag, "rust-v")
	if strings.HasPrefix(release.Tag, "rust-v") {
		if n, _ := fmt.Sscanf(v, "%d.%d.%d", &major, &minor, &patch); n == 3 && v == fmt.Sprintf("%d.%d.%d", major, minor, patch) && major == 0 && minor >= 154 {
			c.version = v
		}
	}
	return c.version
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
	clientVersion := c.clientVersion(ctx)
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
