package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"subai/internal/accounts"
)

// accountHolds exposes explicit, audited release rather than an active-state bypass.
func (s *Server) accountHolds(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		rows, err := s.DB.Pool.Query(r.Context(), `SELECT reason,details,created_at::text FROM account_holds WHERE account_id=$1 ORDER BY reason`, id)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var reason, at string
			var details json.RawMessage
			if err := rows.Scan(&reason, &details, &at); err != nil {
				s.writeErr(w, 500, err.Error())
				return
			}
			out = append(out, map[string]any{"reason": reason, "details": details, "created_at": at})
		}
		if err := rows.Err(); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		s.writeJSON(w, 200, map[string]any{"data": out})
		return
	}
	if r.Method != http.MethodPost {
		s.writeErr(w, 405, "method not allowed")
		return
	}
	var req struct {
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req) != nil || strings.TrimSpace(req.Note) == "" {
		s.writeErr(w, 400, "reason and evidence note required")
		return
	}
	switch req.Reason {
	case "over_reserve", "admin_action", "admin_pause", "legacy_review":
	default:
		s.writeErr(w, 400, "reason requires its own resolution flow")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	if err := accounts.LockTx(r.Context(), tx, id); err != nil {
		s.writeErr(w, 404, "account not found")
		return
	}
	tag, err := tx.Exec(r.Context(), `DELETE FROM account_holds WHERE account_id=$1 AND reason=$2`, id, req.Reason)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.writeErr(w, 404, "hold not found")
		return
	}
	recovered, err := accounts.ResolveUnknownTx(r.Context(), tx, id)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	changes, _ := json.Marshal(map[string]string{"reason": req.Reason, "note": req.Note})
	if _, err := tx.Exec(r.Context(), `INSERT INTO admin_events(actor,action,target_type,target_id,changes) VALUES($1,'account.release_hold','account',$2,$3)`, actorFrom(r), id, changes); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.writeJSON(w, 200, map[string]any{"ok": true, "account_recovered": recovered})
}
