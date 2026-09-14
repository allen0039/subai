package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"subai/internal/egress"
	"subai/internal/storage"
)

type modelSyncRequest struct {
	PoolIDs []string `json:"pool_ids"`
}

// ServiceGroupModelCandidates is the service-group view of upstream
// capabilities.  A price catalogue is intentionally absent here: billing
// configuration never establishes that an upstream can serve a model.
func (s *Server) serviceGroupModelCandidates(w http.ResponseWriter, r *http.Request, groupID string) {
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT c.public_model, COUNT(DISTINCT c.account_id)
		FROM account_model_capabilities c
		JOIN accounts a ON a.id=c.account_id AND a.state='active'
		JOIN account_group_members gm ON gm.account_id=c.account_id
		WHERE gm.group_id=$1 AND c.status='active'
		GROUP BY c.public_model ORDER BY c.public_model`, groupID)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var model string
		var count int
		if err := rows.Scan(&model, &count); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"model": model, "account_count": count})
	}
	if err := rows.Err(); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) syncServiceGroupModels(w http.ResponseWriter, r *http.Request, groupID string) {
	if s.Models == nil || s.Egress == nil {
		s.writeErr(w, http.StatusServiceUnavailable, "上游模型同步服务尚未配置")
		return
	}
	var platform string
	if err := s.DB.Pool.QueryRow(r.Context(), `SELECT platform FROM account_groups WHERE id=$1`, groupID).Scan(&platform); err != nil {
		s.writeErr(w, 404, "服务分组不存在")
		return
	}
	if platform != "codex" {
		s.writeErr(w, 400, "当前仅 Codex 服务分组支持自动同步；其他平台请在上游账号中配置模型能力")
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT DISTINCT a.id::text FROM accounts a JOIN account_group_members gm ON gm.account_id=a.id WHERE gm.group_id=$1 AND a.provider='codex' AND a.state='active' ORDER BY a.id::text`, groupID)
	if err != nil {
		log.Printf("service group model sync could not load accounts: group=%s error=%v", groupID, err)
		s.writeErr(w, 500, "读取服务分组账号失败")
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
		s.writeErr(w, 400, "服务分组没有已启用的 Codex 上游账号")
		return
	}
	updated, failed := 0, 0
	var lastErr error
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := s.syncOneAccountModels(ctx, id)
		cancel()
		if err != nil {
			failed++
			lastErr = err
			continue
		}
		updated++
	}
	if updated == 0 {
		message := modelSyncErrorMessage(lastErr)
		log.Printf("service group model sync failed: group=%s accounts=%d reason=%s", groupID, len(ids), message)
		s.writeErr(w, http.StatusBadGateway, message)
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "service_group.model_sync", "service_group", groupID, storage.SanitizeForAdminEvent(map[string]any{"accounts": updated, "failed": failed}), "")
	s.writeJSON(w, 200, map[string]any{"updated_accounts": updated, "failed_accounts": failed})
}

// modelSyncErrorMessage deliberately keeps the response actionable while
// excluding credentials and upstream response bodies from the admin surface.
func modelSyncErrorMessage(err error) string {
	if err == nil {
		return "未能读取官方模型，请检查账号授权和出口策略后重试"
	}
	message := strings.TrimSpace(err.Error())
	switch {
	case strings.Contains(message, "状态码 401"), strings.Contains(message, "状态码 403"), strings.Contains(message, "凭证"):
		return "官方账号授权已失效，请重新授权后再同步模型"
	case strings.Contains(message, "状态码 429"):
		return "官方模型服务请求过于频繁，请稍后再同步"
	case strings.Contains(message, "状态码 404"):
		return "官方模型服务暂不可用，请稍后再同步"
	case strings.Contains(strings.ToLower(message), "timeout"), strings.Contains(strings.ToLower(message), "deadline"):
		return "连接官方模型服务超时，请检查账号出口策略"
	case strings.Contains(strings.ToLower(message), "proxy"), strings.Contains(strings.ToLower(message), "connection"), strings.Contains(strings.ToLower(message), "network"):
		return "无法连接官方模型服务，请检查账号出口策略"
	case message != "":
		return message
	default:
		return "未能读取官方模型，请检查账号授权和出口策略后重试"
	}
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
		JOIN accounts a ON a.id=c.account_id AND a.state='active'
		JOIN account_group_members gm ON gm.account_id=c.account_id
		WHERE c.status='active' AND gm.group_id = ANY($1::uuid[])
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
		ORDER BY a.id::text`, pools)
	if err != nil {
		log.Printf("account model sync could not load accounts: error=%v", err)
		s.writeErr(w, 500, "读取账号池中的上游账号失败")
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
