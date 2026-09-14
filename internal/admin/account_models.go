package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"subai/internal/egress"
	"subai/internal/storage"
)

type modelSyncRequest struct {
	PoolIDs []string `json:"pool_ids"`
}

func parseModelPoolIDs(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if resourceUUID.MatchString(id) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// listAccountModelCandidates deliberately reads account capability snapshots,
// not model_prices. Price entries describe billing and may include models that
// this Codex account pool cannot call.
func (s *Server) listAccountModelCandidates(w http.ResponseWriter, r *http.Request) {
	pools := parseModelPoolIDs(r.URL.Query().Get("pool_ids"))
	if len(pools) == 0 {
		s.writeJSON(w, 200, map[string]any{"data": []any{}})
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT c.public_model, COUNT(DISTINCT c.account_id)
		FROM account_model_capabilities c
		JOIN accounts a ON a.id=c.account_id AND a.provider='codex' AND a.state='active'
		JOIN account_group_members gm ON gm.account_id=c.account_id
		WHERE c.status='active' AND c.public_model LIKE 'gpt-%' AND gm.group_id = ANY($1::uuid[])
		GROUP BY c.public_model ORDER BY c.public_model`, pools)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var model string
		var accounts int
		if err := rows.Scan(&model, &accounts); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"model": model, "account_count": accounts})
	}
	if err := rows.Err(); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) syncAccountModels(w http.ResponseWriter, r *http.Request) {
	if s.Models == nil || s.Egress == nil {
		s.writeErr(w, http.StatusServiceUnavailable, "上游模型同步服务尚未配置")
		return
	}
	var req modelSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeErr(w, 400, "invalid body")
		return
	}
	pools := parseModelPoolIDs(strings.Join(req.PoolIDs, ","))
	if len(pools) == 0 {
		s.writeErr(w, 400, "请先选择至少一个账号池")
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT DISTINCT a.id::text FROM accounts a
		JOIN account_group_members gm ON gm.account_id=a.id
		WHERE a.provider='codex' AND a.state='active' AND gm.group_id = ANY($1::uuid[])
		ORDER BY a.id`, pools)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		s.writeErr(w, 400, "所选账号池没有已启用的 Codex 上游账号")
		return
	}
	updated, failures := 0, []string{}
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := s.syncOneAccountModels(ctx, id)
		cancel()
		if err != nil {
			failures = append(failures, id)
			continue
		}
		updated++
	}
	if updated == 0 {
		s.writeErr(w, http.StatusBadGateway, "未能从所选 Codex 账号读取模型；请检查授权状态与出口代理")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "account.model_sync", "account_group", "", storage.SanitizeForAdminEvent(map[string]any{"accounts": updated, "failed": len(failures)}), "")
	s.writeJSON(w, 200, map[string]any{"updated_accounts": updated, "failed_accounts": len(failures)})
}

func (s *Server) syncOneAccountModels(ctx context.Context, accountID string) error {
	creds, policyID, expiresAt, err := s.loadQuotaAccount(ctx, accountID)
	if err == nil && expiresAt != nil && time.Until(*expiresAt) < 5*time.Minute && s.OAuth != nil {
		if refreshErr := s.OAuth.Refresh(ctx, accountID); refreshErr != nil && time.Now().After(*expiresAt) {
			err = errors.New("账号凭证已到期，自动刷新失败，请重新授权")
		} else {
			creds, policyID, _, err = s.loadQuotaAccount(ctx, accountID)
		}
	}
	if err != nil {
		return err
	}
	profiles := []egress.Profile{{ID: "direct", Name: "直接连接", Kind: "direct", Status: "active"}}
	if policyID != "" {
		policy, err := s.Egress.GetPolicy(ctx, policyID)
		if err != nil {
			return errors.New("账号出口策略不可用")
		}
		profiles = append([]egress.Profile{policy.Primary}, policy.Fallbacks...)
	}
	var models []string
	var lastErr error
	for _, profile := range profiles {
		client, err := egress.ClientForProfile(profile, 25*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		models, err = s.Models.FetchCodexModels(ctx, client, creds)
		if err == nil {
			break
		}
		lastErr = err
	}
	if len(models) == 0 {
		if lastErr == nil {
			lastErr = errors.New("官方 Codex 未返回模型")
		}
		return lastErr
	}
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM account_model_capabilities WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	for _, model := range models {
		if _, err = tx.Exec(ctx, `INSERT INTO account_model_capabilities(account_id,public_model,upstream_model,status,priority) VALUES($1,$2,$2,'active',100)`, accountID, model); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
