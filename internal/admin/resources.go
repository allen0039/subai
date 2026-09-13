package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"subai/internal/accounts"
	"subai/internal/egress"
	"subai/internal/storage"
)

// ── Proxy profiles ───────────────────────────────────────────────────────────

func (s *Server) listProxies(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at", "name"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT id::text, name, kind, COALESCE(endpoint,''), status, version, created_at::text
		FROM proxy_profiles ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, kind, endpoint, status, created string
		var version int
		if err := rows.Scan(&id, &name, &kind, &endpoint, &status, &version, &created); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "kind": kind, "endpoint": endpoint, "status": status, "version": version, "created_at": created})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createProxy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Endpoint string `json:"endpoint"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		s.writeErr(w, 400, "name required")
		return
	}
	switch req.Kind {
	case "direct":
		req.Endpoint = "" // direct must not carry a proxy address (§16.2)
	case "http", "socks5":
		if req.Endpoint == "" {
			s.writeErr(w, 400, "endpoint required for "+req.Kind)
			return
		}
	default:
		s.writeErr(w, 400, "kind must be direct|http|socks5")
		return
	}
	var sealed []byte
	if req.Username != "" || req.Password != "" {
		plain, _ := json.Marshal(map[string]string{"Username": req.Username, "Password": req.Password})
		var err error
		sealed, err = s.DB.Encrypt(plain)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
	}
	var id string
	err := s.DB.Pool.QueryRow(r.Context(),
		`INSERT INTO proxy_profiles(name, kind, endpoint, credentials_ciphertext) VALUES($1,$2,NULLIF($3,''),$4) RETURNING id::text`,
		req.Name, req.Kind, req.Endpoint, sealed).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "proxy.create", "proxy_profile", id,
		storage.SanitizeForAdminEvent(map[string]any{"kind": req.Kind, "endpoint": req.Endpoint}), "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) patchProxy(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Name             *string `json:"name"`
		Kind             *string `json:"kind"`
		Endpoint         *string `json:"endpoint"`
		Username         *string `json:"username"`
		Password         *string `json:"password"`
		ClearCredentials bool    `json:"clear_credentials"`
		Status           *string `json:"status"`
		Version          int     `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, "begin proxy update failed")
		return
	}
	defer tx.Rollback(r.Context())
	var name, kind, endpoint, status string
	var sealed []byte
	var version int
	err = tx.QueryRow(r.Context(), `SELECT name,kind,COALESCE(endpoint,''),status,credentials_ciphertext,version FROM proxy_profiles WHERE id=$1 FOR UPDATE`, id).Scan(&name, &kind, &endpoint, &status, &sealed, &version)
	if err != nil || version != req.Version {
		s.writeErr(w, 409, "version conflict or proxy not found")
		return
	}
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if req.Kind != nil {
		kind = *req.Kind
	}
	if req.Endpoint != nil {
		endpoint = strings.TrimSpace(*req.Endpoint)
	}
	if req.Status != nil {
		status = *req.Status
	}
	if name == "" || (status != "active" && status != "disabled") {
		s.writeErr(w, 400, "名称不能为空，状态必须为 active 或 disabled")
		return
	}
	switch kind {
	case "direct":
		endpoint = ""
	case "http", "socks5":
		if endpoint == "" {
			s.writeErr(w, 400, "代理地址不能为空")
			return
		}
	default:
		s.writeErr(w, 400, "kind must be direct|http|socks5")
		return
	}
	if req.ClearCredentials || kind == "direct" {
		sealed = nil
	} else if req.Username != nil || req.Password != nil {
		creds := map[string]string{}
		if len(sealed) > 0 {
			plain, err := s.DB.Decrypt(sealed)
			if err != nil {
				s.writeErr(w, 500, "读取代理凭据失败")
				return
			}
			if err := json.Unmarshal(plain, &creds); err != nil {
				s.writeErr(w, 500, "读取代理凭据失败")
				return
			}
		}
		if req.Username != nil {
			creds["Username"] = *req.Username
		}
		if req.Password != nil && *req.Password != "" {
			creds["Password"] = *req.Password
		}
		plain, _ := json.Marshal(creds)
		sealed, err = s.DB.Encrypt(plain)
		if err != nil {
			s.writeErr(w, 500, "保存代理凭据失败")
			return
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE proxy_profiles SET name=$2,kind=$3,endpoint=NULLIF($4,''),status=$5,credentials_ciphertext=$6,version=version+1,updated_at=now() WHERE id=$1`, id, name, kind, endpoint, status, sealed)
	if err != nil {
		s.writeErr(w, 409, "保存代理失败，请检查名称是否重复")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, "commit proxy update failed")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "proxy.update", "proxy_profile", id, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteProxy(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	tag, err := s.DB.Pool.Exec(r.Context(), `DELETE FROM proxy_profiles WHERE id=$1 AND version=$2 AND NOT EXISTS (SELECT 1 FROM egress_policies WHERE primary_proxy_id=$1 OR $1=ANY(ordered_fallback_proxy_ids))`, id, req.Version)
	if err != nil || tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "无法删除：代理仍被出口策略使用，或数据已更新。请先修改相关策略并刷新。")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "proxy.delete", "proxy_profile", id, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

// testProxy probes with a server-fixed target (§17.2: 探测使用服务端固定允许目标);
// the endpoint is never client-chosen, so this cannot become an SSRF tool.
func (s *Server) testProxy(w http.ResponseWriter, r *http.Request, id string) {
	var probeURL = "https://www.gstatic.com/generate_204"
	var kind, endpoint string
	var sealed []byte
	err := s.DB.Pool.QueryRow(r.Context(),
		`SELECT kind, COALESCE(endpoint,''), COALESCE(credentials_ciphertext, ''::bytea) FROM proxy_profiles WHERE id=$1`, id).
		Scan(&kind, &endpoint, &sealed)
	if err != nil {
		s.writeErr(w, 404, "proxy not found")
		return
	}
	prof := proxyProfileForTest(kind, endpoint, sealed, s.DB)
	client, err := egressClientFor(prof)
	if err != nil {
		s.writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	resp, err := client.Get(probeURL)
	if err != nil {
		s.writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	s.writeJSON(w, 200, map[string]any{"ok": resp.StatusCode < 500, "status": resp.StatusCode})
}

// proxyProfileForTest decrypts stored credentials into an egress profile.
func proxyProfileForTest(kind, endpoint string, sealed []byte, db *storage.DB) egress.Profile {
	prof := egress.Profile{Kind: kind, Endpoint: endpoint, Status: "active"}
	if len(sealed) > 0 {
		plain, err := db.Decrypt(sealed)
		if err == nil {
			var c struct{ Username, Password string }
			if json.Unmarshal(plain, &c) == nil {
				prof.Username, prof.Password = c.Username, c.Password
			}
		}
	}
	return prof
}

func egressClientFor(prof egress.Profile) (*http.Client, error) {
	return egress.ClientForProfile(prof, 15*time.Second)
}

// ── Egress policies ──────────────────────────────────────────────────────────

func (s *Server) listEgressPolicies(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at", "name"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT p.id::text, p.name, pp.name, p.failure_mode, p.version, p.created_at::text,
		       COALESCE((SELECT array_agg(x.name) FROM unnest(p.ordered_fallback_proxy_ids) f JOIN proxy_profiles x ON x.id=f), '{}')
		FROM egress_policies p JOIN proxy_profiles pp ON pp.id=p.primary_proxy_id
		ORDER BY p.created_at DESC, p.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, primary, mode, created string
		var version int
		var fallbacks []string
		if err := rows.Scan(&id, &name, &primary, &mode, &version, &created, &fallbacks); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "primary": primary, "failure_mode": mode, "fallbacks": fallbacks, "version": version, "created_at": created})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createEgressPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		PrimaryID   string   `json:"primary_proxy_id"`
		FailureMode string   `json:"failure_mode"`
		FallbackIDs []string `json:"fallback_proxy_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.PrimaryID == "" {
		s.writeErr(w, 400, "name and primary_proxy_id required")
		return
	}
	if req.FailureMode == "" {
		req.FailureMode = "stop"
	}
	if req.FailureMode != "stop" && req.FailureMode != "fallback" {
		s.writeErr(w, 400, "failure_mode must be stop|fallback")
		return
	}
	var id string
	err := s.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO egress_policies(name, primary_proxy_id, failure_mode, ordered_fallback_proxy_ids)
		VALUES($1,$2,$3,$4) RETURNING id::text`,
		req.Name, req.PrimaryID, req.FailureMode, req.FallbackIDs).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "egress_policy.create", "egress_policy", id, nil, "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

// ── Accounts ─────────────────────────────────────────────────────────────────

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at", "label"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT a.id::text, a.provider, a.label, a.state, a.concurrency_limit, a.priority,
		       COALESCE(a.egress_policy_id::text,''), a.credential_version, COALESCE(a.expires_at::text,''),
		       a.version, a.created_at::text, COALESCE(ep.name,'未配置'), COALESCE(pp.name,'未配置')
		FROM accounts a LEFT JOIN egress_policies ep ON ep.id=a.egress_policy_id LEFT JOIN proxy_profiles pp ON pp.id=ep.primary_proxy_id ORDER BY a.created_at DESC, a.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, provider, label, state, egress, expires, created, policyName, proxyName string
		var limit, priority, credVersion, version int
		if err := rows.Scan(&id, &provider, &label, &state, &limit, &priority, &egress, &credVersion, &expires, &version, &created, &policyName, &proxyName); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "provider": provider, "label": label, "state": state,
			"concurrency_limit": limit, "priority": priority, "egress_policy_id": egress,
			"egress_policy_name": policyName, "proxy_name": proxyName,
			"credential_version": credVersion, "expires_at": expires, "version": version, "created_at": created,
		})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label            string `json:"label"`
		ConcurrencyLimit *int   `json:"concurrency_limit"`
		Priority         *int   `json:"priority"`
		EgressPolicyID   string `json:"egress_policy_id"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		AccountID        string `json:"account_id"`
		ExpiresAt        string `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Label == "" || req.AccessToken == "" {
		s.writeErr(w, 400, "label and access_token required")
		return
	}
	limit, priority := 1, 100
	if req.ConcurrencyLimit != nil {
		limit = *req.ConcurrencyLimit
	}
	if req.Priority != nil {
		priority = *req.Priority
	}
	creds := map[string]string{"access_token": req.AccessToken, "refresh_token": req.RefreshToken, "account_id": req.AccountID}
	plain, _ := json.Marshal(creds)
	sealed, err := s.DB.Encrypt(plain)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	var id string
	err = s.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO accounts(label, credentials_ciphertext, concurrency_limit, priority, egress_policy_id)
		VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid) RETURNING id::text`,
		req.Label, sealed, limit, priority, req.EgressPolicyID).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "account.create", "account", id,
		storage.SanitizeForAdminEvent(map[string]any{"label": req.Label, "concurrency_limit": limit}), "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) patchAccount(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Label            *string `json:"label"`
		ProxyID          *string `json:"proxy_id"`
		State            *string `json:"state"`
		ConcurrencyLimit *int    `json:"concurrency_limit"`
		Priority         *int    `json:"priority"`
		EgressPolicyID   *string `json:"egress_policy_id"`
		Version          int     `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	allowed := map[string]bool{"": true, "active": true, "paused": true, "reauth_required": true,
		"quota_exhausted": true, "recovery_hold": true}
	if !allowed[strings.TrimSpace(deref(req.State))] {
		s.writeErr(w, 400, "invalid state")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, "begin account update failed")
		return
	}
	defer tx.Rollback(r.Context())
	if err := accounts.LockTx(r.Context(), tx, id); err != nil {
		s.writeErr(w, 404, "account not found")
		return
	}
	// Activation and hold writers serialize on the same account row.
	if req.State != nil && *req.State == "active" {
		var holdCount int
		err := tx.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM account_holds WHERE account_id = $1`, id).Scan(&holdCount)
		if err != nil {
			s.writeErr(w, 500, "failed to check holds")
			return
		}
		if holdCount > 0 {
			s.writeErr(w, 409, "cannot set state to active: account has unresolved hold reasons")
			return
		}
	}
	if req.Label != nil && strings.TrimSpace(*req.Label) == "" {
		s.writeErr(w, 400, "标签不能为空")
		return
	}
	if req.ConcurrencyLimit != nil && *req.ConcurrencyLimit < 1 {
		s.writeErr(w, 400, "并发上限至少为 1")
		return
	}
	if req.ProxyID != nil {
		if !resourceUUID.MatchString(*req.ProxyID) || req.EgressPolicyID != nil {
			s.writeErr(w, 400, "请选择有效代理，不能同时指定出口策略")
			return
		}
		var policyID string
		err := tx.QueryRow(r.Context(), `INSERT INTO egress_policies(name,primary_proxy_id,failure_mode) SELECT $2,id,'stop' FROM proxy_profiles WHERE id=$1 AND status='active' RETURNING id::text`, *req.ProxyID, "account-"+id+"-"+time.Now().Format("20060102150405.000000000")).Scan(&policyID)
		if err != nil {
			s.writeErr(w, 400, "代理不存在或已禁用")
			return
		}
		req.EgressPolicyID = &policyID
	}
	var egress any
	if req.EgressPolicyID != nil {
		egress = *req.EgressPolicyID
	}
	tag, err := tx.Exec(r.Context(), `
		UPDATE accounts SET
			state = COALESCE(NULLIF($2,''), state),
			concurrency_limit = COALESCE($3, concurrency_limit),
			priority = COALESCE($4, priority),
			egress_policy_id = CASE WHEN $5::text IS NULL THEN egress_policy_id ELSE NULLIF($5::text,'')::uuid END,
            label = COALESCE($7,label),
			version = version + 1, updated_at = now()
		WHERE id=$1 AND version=$6`,
		id, deref(req.State), req.ConcurrencyLimit, req.Priority, egress, req.Version, req.Label)
	if err != nil || tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "version conflict or account not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, "commit account update failed")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "account.update", "account", id,
		storage.SanitizeForAdminEvent(map[string]any{"state": deref(req.State), "version": req.Version}), "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ── Account groups & routes ──────────────────────────────────────────────────

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	limit, offset, _ := pageParams(r, []string{"created_at", "name"})
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT g.id::text, g.name, g.description, g.strategy, g.status, g.version,
		       COALESCE((SELECT array_agg(a.label ORDER BY a.priority) FROM account_group_members m
		                 JOIN accounts a ON a.id=m.account_id WHERE m.group_id=g.id), '{}')
		FROM account_groups g ORDER BY g.created_at DESC, g.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, description, strategy, status string
		var version int
		var accountsList []string
		if err := rows.Scan(&id, &name, &description, &strategy, &status, &version, &accountsList); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "description": description, "strategy": strategy, "status": status, "version": version, "accounts": accountsList})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Strategy    string `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		s.writeErr(w, 400, "name required")
		return
	}
	if req.Strategy == "" {
		req.Strategy = "round_robin"
	}
	if req.Strategy != "round_robin" && req.Strategy != "weighted_round_robin" && req.Strategy != "priority_failover" {
		s.writeErr(w, 400, "strategy must be round_robin, weighted_round_robin or priority_failover")
		return
	}
	var id string
	if err := s.DB.Pool.QueryRow(r.Context(),
		`INSERT INTO account_groups(name,description,strategy) VALUES($1,$2,$3) RETURNING id::text`, req.Name, req.Description, req.Strategy).Scan(&id); err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "group.create", "account_group", id, nil, "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) groupAddAccount(w http.ResponseWriter, r *http.Request, groupID string) {
	var req struct {
		AccountID string `json:"account_id"`
		Weight    int    `json:"weight"`
		Priority  int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !resourceUUID.MatchString(req.AccountID) {
		s.writeErr(w, 400, "valid account_id required")
		return
	}
	if req.Weight <= 0 {
		req.Weight = 1
	}
	if _, err := s.DB.Pool.Exec(r.Context(),
		`INSERT INTO account_group_members(group_id, account_id, weight, priority) VALUES($1,$2,$3,$4)
		 ON CONFLICT (group_id,account_id) DO UPDATE SET weight=EXCLUDED.weight, priority=EXCLUDED.priority`,
		groupID, req.AccountID, req.Weight, req.Priority); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "group.add_account", "account_group", groupID,
		map[string]any{"account_id": req.AccountID}, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) keyRoutes(w http.ResponseWriter, r *http.Request, keyID string) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.DB.Pool.Query(r.Context(), `
			SELECT id::text, target_type, target_id::text, priority FROM key_routes WHERE api_key_id=$1`, keyID)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, ttype, tid string
			var prio int
			if err := rows.Scan(&id, &ttype, &tid, &prio); err != nil {
				s.writeErr(w, 500, err.Error())
				return
			}
			out = append(out, map[string]any{"id": id, "target_type": ttype, "target_id": tid, "priority": prio})
		}
		s.writeJSON(w, 200, map[string]any{"data": out})
	case http.MethodPost:
		var req struct {
			TargetType string `json:"target_type"`
			TargetID   string `json:"target_id"`
			Priority   int    `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.writeErr(w, 400, "invalid body")
			return
		}
		if req.TargetType != "account" && req.TargetType != "group" {
			s.writeErr(w, 400, "target_type must be account|group")
			return
		}
		if !resourceUUID.MatchString(req.TargetID) {
			s.writeErr(w, 400, "invalid target_id")
			return
		}
		if req.Priority == 0 {
			req.Priority = 100
		}
		var id string
		err := s.DB.Pool.QueryRow(r.Context(), `
			INSERT INTO key_routes(api_key_id, target_type, target_id, priority) VALUES($1,$2,$3,$4) RETURNING id::text`,
			keyID, req.TargetType, req.TargetID, req.Priority).Scan(&id)
		if err != nil {
			s.writeErr(w, 409, err.Error())
			return
		}
		s.notifyMutation()
		s.DB.LogAdminEvent(r.Context(), actorFrom(r), "route.create", "key_route", id, nil, keyID)
		s.writeJSON(w, 201, map[string]any{"id": id})
	case http.MethodDelete:
		routeID := r.URL.Query().Get("route_id")
		if !resourceUUID.MatchString(routeID) {
			s.writeErr(w, 400, "invalid route_id")
			return
		}
		if _, err := s.DB.Pool.Exec(r.Context(), `DELETE FROM key_routes WHERE id=$1 AND api_key_id=$2`, routeID, keyID); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		s.notifyMutation()
		s.DB.LogAdminEvent(r.Context(), actorFrom(r), "route.delete", "key_route", routeID, nil, keyID)
		s.writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		s.writeErr(w, 405, "method not allowed")
	}
}
