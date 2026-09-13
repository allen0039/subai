package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"subai/internal/auth"
	"subai/internal/storage"
)

// ── Members ──────────────────────────────────────────────────────────────────

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at", "name"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT m.id::text, m.name, m.role, m.status, m.created_at::text, m.updated_at::text, m.version,
		       (SELECT count(*) FROM api_keys k WHERE k.member_id=m.id AND k.status <> 'revoked'),
		       (SELECT count(*) FROM user_subscriptions us WHERE us.member_id=m.id AND us.status IN ('scheduled','active','suspended'))
		FROM members m ORDER BY m.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type member struct {
		ID                string `json:"id"`
		Name              string `json:"name"`
		Role              string `json:"role"`
		Status            string `json:"status"`
		CreatedAt         string `json:"created_at"`
		UpdatedAt         string `json:"updated_at"`
		Version           int    `json:"version"`
		KeyCount          int    `json:"key_count"`
		SubscriptionCount int    `json:"subscription_count"`
	}
	out := []member{}
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.ID, &m.Name, &m.Role, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.Version, &m.KeyCount, &m.SubscriptionCount); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, m)
	}
	s.writeJSON(w, 200, map[string]any{"data": out, "limit": limit, "offset": offset})
}

func (s *Server) createMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		s.writeErr(w, 400, "name required")
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}
	if req.Role != "admin" && req.Role != "member" {
		s.writeErr(w, 400, "role must be admin or member")
		return
	}
	if req.Password == "" {
		s.writeErr(w, 400, "password required (no default passwords)")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	var id string
	err = s.DB.Pool.QueryRow(r.Context(),
		`INSERT INTO members(name, role, password_hash) VALUES($1,$2,$3) RETURNING id::text`,
		req.Name, req.Role, hash).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, "name conflict or invalid: "+err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "member.create", "member", id,
		storage.SanitizeForAdminEvent(map[string]any{"name": req.Name, "role": req.Role}), "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) patchMember(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Name     *string `json:"name"`
		Role     *string `json:"role"`
		Status   *string `json:"status"`
		Password *string `json:"password"`
		Version  int     `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	sets := []string{"version = version + 1"}
	// Keep the record id at $1.  Dynamic fields then consume $2 onward and
	// the optimistic-lock version is appended last.  Starting with an empty
	// argument list made the first submitted field bind to $2 while $1 had no
	// context/type, which PostgreSQL correctly rejects (SQLSTATE 42P18).
	args := []any{id}
	if req.Name != nil {
		sets = append(sets, "name=$"+itoa(len(args)+1))
		args = append(args, *req.Name)
	}
	if req.Role != nil {
		if *req.Role != "admin" && *req.Role != "member" {
			s.writeErr(w, 400, "role must be admin or member")
			return
		}
		sets = append(sets, "role=$"+itoa(len(args)+1))
		args = append(args, *req.Role)
	}
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "disabled" {
			s.writeErr(w, 400, "status must be active or disabled")
			return
		}
		sets = append(sets, "status=$"+itoa(len(args)+1))
		args = append(args, *req.Status)
	}
	if req.Password != nil {
		if strings.TrimSpace(*req.Password) == "" {
			s.writeErr(w, 400, "password must not be empty")
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		sets = append(sets, "password_hash=$"+itoa(len(args)+1))
		args = append(args, hash)
	}
	// Never allow an update to remove the final active administrator.
	if (req.Role != nil && *req.Role != "admin") || (req.Status != nil && *req.Status != "active") {
		var currentRole, currentStatus string
		if err := s.DB.Pool.QueryRow(r.Context(), `SELECT role,status FROM members WHERE id=$1`, id).Scan(&currentRole, &currentStatus); err != nil {
			s.writeErr(w, 404, "member not found")
			return
		}
		if currentRole == "admin" && currentStatus == "active" {
			var n int
			if err := s.DB.Pool.QueryRow(r.Context(), `SELECT count(*) FROM members WHERE role='admin' AND status='active'`).Scan(&n); err != nil {
				s.writeErr(w, 500, err.Error())
				return
			}
			if n <= 1 {
				s.writeErr(w, 409, "cannot disable or demote the last active administrator")
				return
			}
		}
	}
	args = append(args, req.Version)
	tag, err := s.DB.Pool.Exec(r.Context(),
		`UPDATE members SET `+strings.Join(sets, ", ")+` WHERE id=$1 AND version=$`+itoa(len(args)), args...)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "version conflict or member not found")
		return
	}
	s.notifyMutation()
	if req.Password != nil || (req.Status != nil && *req.Status == "disabled") || (req.Role != nil && *req.Role != "admin") {
		if id == actorFrom(r) && req.Password != nil {
			_ = s.Auth.RevokeOtherMemberSessions(r.Context(), r, id)
		} else {
			_ = s.Auth.RevokeMemberSessions(r.Context(), id)
		}
	}
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "member.update", "member", id,
		storage.SanitizeForAdminEvent(map[string]any{"version": req.Version}), "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ── Clients ──────────────────────────────────────────────────────────────────

func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at", "name"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT c.id::text, c.member_id::text, c.name, c.type, c.notes, c.status, c.version,
		       (SELECT count(*) FROM api_keys k WHERE k.client_id=c.id) AS key_count
		FROM clients c ORDER BY c.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type client struct {
		ID       string `json:"id"`
		MemberID string `json:"member_id"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Notes    string `json:"notes"`
		Status   string `json:"status"`
		Version  int    `json:"version"`
		KeyCount int    `json:"key_count"`
	}
	out := []client{}
	for rows.Next() {
		var c client
		if err := rows.Scan(&c.ID, &c.MemberID, &c.Name, &c.Type, &c.Notes, &c.Status, &c.Version, &c.KeyCount); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, c)
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MemberID string `json:"member_id"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Notes    string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MemberID == "" || req.Name == "" {
		s.writeErr(w, 400, "member_id and name required")
		return
	}
	switch req.Type {
	case "computer", "hermes", "cli", "other":
	default:
		s.writeErr(w, 400, "type must be computer|hermes|cli|other")
		return
	}
	var id string
	err := s.DB.Pool.QueryRow(r.Context(),
		`INSERT INTO clients(member_id, name, type, notes) VALUES($1,$2,$3,$4) RETURNING id::text`,
		req.MemberID, req.Name, req.Type, req.Notes).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "client.create", "client", id, nil, "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) patchClient(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Name    *string `json:"name"`
		Type    *string `json:"type"`
		Notes   *string `json:"notes"`
		Status  *string `json:"status"`
		Version int     `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	tag, err := s.DB.Pool.Exec(r.Context(), `
		UPDATE clients SET
			name   = COALESCE($2, name),
			type   = COALESCE($3, type),
			notes  = COALESCE($4, notes),
			status = COALESCE($5, status),
			version = version + 1, updated_at = now()
		WHERE id=$1 AND version=$6`,
		id, req.Name, req.Type, req.Notes, req.Status, req.Version)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "version conflict or client not found")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "client.update", "client", id, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

// ── API Keys ─────────────────────────────────────────────────────────────────

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at", "name"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT k.id::text, k.member_id::text, COALESCE(k.client_id::text,''), k.name, k.public_prefix,
		       k.status, COALESCE(k.expires_at::text,''), k.concurrency_limit, k.allowed_models, k.version,
		       k.created_at::text
		FROM api_keys k ORDER BY k.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type key struct {
		ID               string   `json:"id"`
		MemberID         string   `json:"member_id"`
		ClientID         string   `json:"client_id"`
		Name             string   `json:"name"`
		PublicPrefix     string   `json:"public_prefix"`
		Status           string   `json:"status"`
		ExpiresAt        string   `json:"expires_at"`
		ConcurrencyLimit int      `json:"concurrency_limit"`
		AllowedModels    []string `json:"allowed_models"`
		Version          int      `json:"version"`
		CreatedAt        string   `json:"created_at"`
	}
	out := []key{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.ID, &k.MemberID, &k.ClientID, &k.Name, &k.PublicPrefix, &k.Status,
			&k.ExpiresAt, &k.ConcurrencyLimit, &k.AllowedModels, &k.Version, &k.CreatedAt); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, k)
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

// createKey generates the plaintext once (§16.2: 密钥仅创建时显示一次).
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MemberID         string   `json:"member_id"`
		ClientID         string   `json:"client_id"`
		Name             string   `json:"name"`
		ExpiresAt        string   `json:"expires_at"`
		ConcurrencyLimit *int     `json:"concurrency_limit"`
		AllowedModels    []string `json:"allowed_models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MemberID == "" || req.Name == "" {
		s.writeErr(w, 400, "member_id and name required")
		return
	}
	limit := 1
	if req.ConcurrencyLimit != nil && *req.ConcurrencyLimit > 0 {
		limit = *req.ConcurrencyLimit
	}
	secret := "sk-subai-" + randToken(30)
	prefix := secret[:16]
	var clientID *string
	if req.ClientID != "" {
		clientID = &req.ClientID
	}
	var id string
	err := s.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO api_keys(member_id, client_id, name, public_prefix, key_hash, concurrency_limit, allowed_models)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`,
		req.MemberID, clientID, req.Name, prefix, storage.HashToken(secret), limit, req.AllowedModels).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "key.create", "api_key", id,
		storage.SanitizeForAdminEvent(map[string]any{"name": req.Name, "concurrency_limit": limit}), "")
	s.writeJSON(w, 201, map[string]any{"id": id, "key": secret, "public_prefix": prefix, "note": "store this key now; it is not retrievable later"})
}

func (s *Server) patchKey(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Name             *string   `json:"name"`
		Status           *string   `json:"status"`
		ExpiresAt        *string   `json:"expires_at"`
		ConcurrencyLimit *int      `json:"concurrency_limit"`
		AllowedModels    *[]string `json:"allowed_models"`
		Version          int       `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	tag, err := s.DB.Pool.Exec(r.Context(), `
		UPDATE api_keys SET
			name              = COALESCE($2, name),
			status            = COALESCE($3, status),
			concurrency_limit = COALESCE($4, concurrency_limit),
			allowed_models    = COALESCE($5, allowed_models),
			version = version + 1, updated_at = now()
		WHERE id=$1 AND version=$6`,
		id, req.Name, req.Status, req.ConcurrencyLimit, req.AllowedModels, req.Version)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "version conflict or key not found")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "key.update", "api_key", id,
		storage.SanitizeForAdminEvent(map[string]any{"version": req.Version}), "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request, id string) {
	tag, err := s.DB.Pool.Exec(r.Context(), `
		UPDATE api_keys SET status='revoked', version=version+1, updated_at=now() WHERE id=$1 AND status<>'revoked'`, id)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "key not found or already revoked")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "key.revoke", "api_key", id, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}
