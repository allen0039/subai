package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"subai/internal/storage"
)

// Model rules are explicit only when configured: an account/group with no rows
// retains the legacy unrestricted behavior.  This lets operators migrate a
// running deployment one upstream at a time.
type modelCapabilityInput struct {
	PublicModel   string `json:"public_model"`
	UpstreamModel string `json:"upstream_model"`
	Status        string `json:"status"`
	Priority      int    `json:"priority"`
}

func normalizeModelName(value string) string { return strings.TrimSpace(value) }

func validModelName(value string) bool {
	return value != "" && len(value) <= 200
}

func (s *Server) accountModelCapabilities(w http.ResponseWriter, r *http.Request, accountID string) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.DB.Pool.Query(r.Context(), `SELECT public_model,upstream_model,status,priority,version,updated_at::text FROM account_model_capabilities WHERE account_id=$1 ORDER BY priority,public_model`, accountID)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var public, upstream, status, updated string
			var priority, version int
			if err := rows.Scan(&public, &upstream, &status, &priority, &version, &updated); err != nil {
				s.writeErr(w, 500, err.Error())
				return
			}
			out = append(out, map[string]any{"public_model": public, "upstream_model": upstream, "status": status, "priority": priority, "version": version, "updated_at": updated})
		}
		s.writeJSON(w, 200, map[string]any{"data": out})
	case http.MethodPost:
		var in modelCapabilityInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			s.writeErr(w, 400, "invalid body")
			return
		}
		in.PublicModel, in.UpstreamModel, in.Status = normalizeModelName(in.PublicModel), normalizeModelName(in.UpstreamModel), normalizeModelName(in.Status)
		if !validModelName(in.PublicModel) || !validModelName(in.UpstreamModel) {
			s.writeErr(w, 400, "public_model and upstream_model are required (max 200 characters)")
			return
		}
		if in.Status == "" {
			in.Status = "active"
		}
		if in.Status != "active" && in.Status != "disabled" {
			s.writeErr(w, 400, "status must be active or disabled")
			return
		}
		_, err := s.DB.Pool.Exec(r.Context(), `INSERT INTO account_model_capabilities(account_id,public_model,upstream_model,status,priority) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id,public_model) DO UPDATE SET upstream_model=EXCLUDED.upstream_model,status=EXCLUDED.status,priority=EXCLUDED.priority,version=account_model_capabilities.version+1,updated_at=now()`, accountID, in.PublicModel, in.UpstreamModel, in.Status, in.Priority)
		if err != nil {
			s.writeErr(w, 409, err.Error())
			return
		}
		s.notifyMutation()
		s.DB.LogAdminEvent(r.Context(), actorFrom(r), "account.model_capability.upsert", "account", accountID, storage.SanitizeForAdminEvent(map[string]any{"public_model": in.PublicModel, "upstream_model": in.UpstreamModel, "status": in.Status}), "")
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodDelete:
		model := normalizeModelName(r.URL.Query().Get("model"))
		if !validModelName(model) {
			s.writeErr(w, 400, "model query parameter required")
			return
		}
		_, err := s.DB.Pool.Exec(r.Context(), `DELETE FROM account_model_capabilities WHERE account_id=$1 AND public_model=$2`, accountID, model)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		s.notifyMutation()
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		s.writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) groupModelRules(w http.ResponseWriter, r *http.Request, groupID string) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.DB.Pool.Query(r.Context(), `SELECT public_model,status,version,updated_at::text FROM account_group_model_rules WHERE group_id=$1 ORDER BY public_model`, groupID)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var model, status, updated string
			var version int
			if err := rows.Scan(&model, &status, &version, &updated); err != nil {
				s.writeErr(w, 500, err.Error())
				return
			}
			out = append(out, map[string]any{"public_model": model, "status": status, "version": version, "updated_at": updated})
		}
		s.writeJSON(w, 200, map[string]any{"data": out})
	case http.MethodPost:
		var in struct {
			PublicModel string `json:"public_model"`
			Status      string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			s.writeErr(w, 400, "invalid body")
			return
		}
		in.PublicModel, in.Status = normalizeModelName(in.PublicModel), normalizeModelName(in.Status)
		if !validModelName(in.PublicModel) {
			s.writeErr(w, 400, "public_model required (max 200 characters)")
			return
		}
		if in.Status == "" {
			in.Status = "active"
		}
		if in.Status != "active" && in.Status != "disabled" {
			s.writeErr(w, 400, "status must be active or disabled")
			return
		}
		_, err := s.DB.Pool.Exec(r.Context(), `INSERT INTO account_group_model_rules(group_id,public_model,status) VALUES($1,$2,$3) ON CONFLICT(group_id,public_model) DO UPDATE SET status=EXCLUDED.status,version=account_group_model_rules.version+1,updated_at=now()`, groupID, in.PublicModel, in.Status)
		if err != nil {
			s.writeErr(w, 409, err.Error())
			return
		}
		s.notifyMutation()
		s.DB.LogAdminEvent(r.Context(), actorFrom(r), "account_group.model_rule.upsert", "account_group", groupID, storage.SanitizeForAdminEvent(map[string]any{"public_model": in.PublicModel, "status": in.Status}), "")
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodDelete:
		model := normalizeModelName(r.URL.Query().Get("model"))
		if !validModelName(model) {
			s.writeErr(w, 400, "model query parameter required")
			return
		}
		if _, err := s.DB.Pool.Exec(r.Context(), `DELETE FROM account_group_model_rules WHERE group_id=$1 AND public_model=$2`, groupID, model); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		s.notifyMutation()
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		s.writeErr(w, 405, "method not allowed")
	}
}
