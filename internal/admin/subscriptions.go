package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"

	"subai/internal/auth"
	"subai/internal/storage"
)

// planInput is intentionally string-based for money: JSON floating-point
// values must never enter the accounting configuration path.
type planInput struct {
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	DailyLimitUSD       *string  `json:"daily_limit_usd"`
	WeeklyLimitUSD      *string  `json:"weekly_limit_usd"`
	MonthlyLimitUSD     *string  `json:"monthly_limit_usd"`
	RateMultiplier      *string  `json:"rate_multiplier"`
	ConcurrencyLimit    int      `json:"concurrency_limit"`
	MaxKeys             int      `json:"max_keys"`
	AllowedModels       []string `json:"allowed_models"`
	DefaultValidityDays int      `json:"default_validity_days"`
	Timezone            string   `json:"timezone"`
	PoolIDs             []string `json:"pool_ids"`
}

func moneyArg(v *string) (any, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, nil
	}
	d, err := decimal.NewFromString(strings.TrimSpace(*v))
	if err != nil || d.IsNegative() {
		return nil, fmt.Errorf("amount must be a non-negative decimal")
	}
	return d.StringFixed(12), nil
}

func normalizePlanInput(in *planInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("name required")
	}
	if in.ConcurrencyLimit <= 0 {
		in.ConcurrencyLimit = 1
	}
	if in.MaxKeys <= 0 {
		in.MaxKeys = 1
	}
	if in.DefaultValidityDays <= 0 {
		in.DefaultValidityDays = 30
	}
	if in.Timezone == "" {
		in.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return fmt.Errorf("invalid timezone")
	}
	for _, v := range []*string{in.DailyLimitUSD, in.WeeklyLimitUSD, in.MonthlyLimitUSD} {
		if _, err := moneyArg(v); err != nil {
			return err
		}
	}
	if in.RateMultiplier == nil || strings.TrimSpace(*in.RateMultiplier) == "" {
		value := "1"
		in.RateMultiplier = &value
	}
	multiplier, err := decimal.NewFromString(strings.TrimSpace(*in.RateMultiplier))
	if err != nil || multiplier.IsNegative() {
		return fmt.Errorf("rate_multiplier must be a non-negative decimal")
	}
	in.RateMultiplier = ptrString(multiplier.StringFixed(12))
	return nil
}

func ptrString(value string) *string { return &value }

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT p.id::text,p.name,p.description,p.status,COALESCE(p.current_version_id::text,''),p.version,p.created_at::text,
		       COALESCE(v.version_number,0), COALESCE(v.daily_limit_usd::text,''), COALESCE(v.weekly_limit_usd::text,''), COALESCE(v.monthly_limit_usd::text,''),
		       COALESCE(v.concurrency_limit,0), COALESCE(v.max_keys,0), v.allowed_models,
		       COALESCE(v.default_validity_days,0), COALESCE(v.timezone,''), COALESCE(v.rate_multiplier,1)::text
		FROM plans p LEFT JOIN plan_versions v ON v.id=p.current_version_id
		ORDER BY p.created_at DESC`)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, desc, status, versionID, created, daily, weekly, monthly, tz, rateMultiplier string
		var planRowVersion, planVersion, concurrency, maxKeys, validity int
		var models []string
		if err := rows.Scan(&id, &name, &desc, &status, &versionID, &planRowVersion, &created, &planVersion, &daily, &weekly, &monthly, &concurrency, &maxKeys, &models, &validity, &tz, &rateMultiplier); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		pools, err := s.poolSummaries(r, versionID)
		if err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "description": desc, "status": status, "current_version_id": versionID, "version": planRowVersion, "plan_version": planVersion, "created_at": created, "daily_limit_usd": daily, "weekly_limit_usd": weekly, "monthly_limit_usd": monthly, "rate_multiplier": rateMultiplier, "concurrency_limit": concurrency, "max_keys": maxKeys, "allowed_models": models, "default_validity_days": validity, "timezone": tz, "pools": pools})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) poolSummaries(r *http.Request, versionID string) ([]map[string]any, error) {
	if versionID == "" {
		return []map[string]any{}, nil
	}
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT g.id::text,g.name,g.strategy,b.priority,COUNT(m.account_id)
		FROM plan_pool_bindings b JOIN account_groups g ON g.id=b.pool_id
		LEFT JOIN account_group_members m ON m.group_id=g.id
		WHERE b.plan_version_id=$1 GROUP BY g.id,g.name,g.strategy,b.priority ORDER BY b.priority,g.name`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, strategy string
		var priority, count int
		if err := rows.Scan(&id, &name, &strategy, &priority, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "strategy": strategy, "priority": priority, "account_count": count})
	}
	return out, rows.Err()
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	var in planInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || normalizePlanInput(&in) != nil {
		s.writeErr(w, 400, "valid plan name, limits and timezone required")
		return
	}
	daily, err := moneyArg(in.DailyLimitUSD)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	weekly, err := moneyArg(in.WeeklyLimitUSD)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	monthly, err := moneyArg(in.MonthlyLimitUSD)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var planID, versionID string
	if err := tx.QueryRow(r.Context(), `INSERT INTO plans(name,description,created_by) VALUES($1,$2,$3) RETURNING id::text`, in.Name, in.Description, actorFrom(r)).Scan(&planID); err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	if err := tx.QueryRow(r.Context(), `
		INSERT INTO plan_versions(plan_id,version_number,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,rate_multiplier,concurrency_limit,max_keys,allowed_models,default_validity_days,timezone,created_by)
		VALUES($1,1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id::text`,
		planID, daily, weekly, monthly, *in.RateMultiplier, in.ConcurrencyLimit, in.MaxKeys, in.AllowedModels, in.DefaultValidityDays, in.Timezone, actorFrom(r)).Scan(&versionID); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := bindPools(r, tx, versionID, in.PoolIDs); err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE plans SET current_version_id=$2,updated_at=now() WHERE id=$1`, planID, versionID); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "plan.create", "plan", planID, storage.SanitizeForAdminEvent(map[string]any{"name": in.Name}), "")
	s.writeJSON(w, 201, map[string]any{"id": planID, "version_id": versionID, "status": "draft"})
}

func bindPools(r *http.Request, tx pgx.Tx, versionID string, poolIDs []string) error {
	seen := map[string]bool{}
	for i, id := range poolIDs {
		if !resourceUUID.MatchString(id) || seen[id] {
			return fmt.Errorf("invalid or duplicate account pool")
		}
		seen[id] = true
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM account_groups WHERE id=$1)`, id).Scan(&exists); err != nil || !exists {
			return fmt.Errorf("account pool not found")
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO plan_pool_bindings(plan_version_id,pool_id,priority) VALUES($1,$2,$3)`, versionID, id, 100+i); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) publishPlan(w http.ResponseWriter, r *http.Request, id string) {
	var versionID string
	if err := s.DB.Pool.QueryRow(r.Context(), `SELECT current_version_id::text FROM plans WHERE id=$1`, id).Scan(&versionID); err != nil || versionID == "" {
		s.writeErr(w, 404, "plan/version not found")
		return
	}
	var healthy int
	if err := s.DB.Pool.QueryRow(r.Context(), `
		SELECT count(*) FROM plan_pool_bindings b JOIN account_groups g ON g.id=b.pool_id
		JOIN account_group_members m ON m.group_id=g.id JOIN accounts a ON a.id=m.account_id
		WHERE b.plan_version_id=$1 AND g.status='active' AND a.state='active'`, versionID).Scan(&healthy); err != nil || healthy == 0 {
		s.writeErr(w, 409, "plan needs at least one active account in a bound account pool")
		return
	}
	if _, err := s.DB.Pool.Exec(r.Context(), `UPDATE plans SET status='active',version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "plan.publish", "plan", id, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) createPlanVersion(w http.ResponseWriter, r *http.Request, planID string) {
	var in planInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.writeErr(w, 400, "invalid body")
		return
	}
	if in.Name == "" {
		in.Name = "version"
	}
	if err := normalizePlanInput(&in); err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	daily, _ := moneyArg(in.DailyLimitUSD)
	weekly, _ := moneyArg(in.WeeklyLimitUSD)
	monthly, _ := moneyArg(in.MonthlyLimitUSD)
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var n int
	if err := tx.QueryRow(r.Context(), `SELECT COALESCE(MAX(version_number),0)+1 FROM plan_versions WHERE plan_id=$1`, planID).Scan(&n); err != nil {
		s.writeErr(w, 404, "plan not found")
		return
	}
	var versionID string
	if err := tx.QueryRow(r.Context(), `INSERT INTO plan_versions(plan_id,version_number,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,rate_multiplier,concurrency_limit,max_keys,allowed_models,default_validity_days,timezone,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id::text`, planID, n, daily, weekly, monthly, *in.RateMultiplier, in.ConcurrencyLimit, in.MaxKeys, in.AllowedModels, in.DefaultValidityDays, in.Timezone, actorFrom(r)).Scan(&versionID); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := bindPools(r, tx, versionID, in.PoolIDs); err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE plans SET current_version_id=$2,version=version+1,updated_at=now() WHERE id=$1`, planID, versionID); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.writeJSON(w, 201, map[string]any{"id": versionID, "version_number": n})
}

type subscriptionInput struct {
	MemberID              string   `json:"member_id"`
	PlanID                string   `json:"plan_id"`
	PlanVersionID         string   `json:"plan_version_id"`
	StartsAt              string   `json:"starts_at"`
	ExpiresAt             string   `json:"expires_at"`
	DailyLimitUSD         *string  `json:"daily_limit_usd"`
	WeeklyLimitUSD        *string  `json:"weekly_limit_usd"`
	MonthlyLimitUSD       *string  `json:"monthly_limit_usd"`
	ConcurrencyOverride   *int     `json:"concurrency_override"`
	MaxKeysOverride       *int     `json:"max_keys_override"`
	AllowedModelsOverride []string `json:"allowed_models_override"`
	Notes                 string   `json:"notes"`
}

func parseWhen(v string, fallback time.Time) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return fallback, nil
	}
	return time.Parse(time.RFC3339, v)
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	var in subscriptionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !resourceUUID.MatchString(in.MemberID) {
		s.writeErr(w, 400, "valid member_id required")
		return
	}
	starts, err := parseWhen(in.StartsAt, time.Now().UTC())
	if err != nil {
		s.writeErr(w, 400, "invalid starts_at")
		return
	}
	var versionID string
	var days int
	var tz string
	if in.PlanVersionID != "" {
		versionID = in.PlanVersionID
	} else if resourceUUID.MatchString(in.PlanID) {
		err = s.DB.Pool.QueryRow(r.Context(), `SELECT current_version_id::text FROM plans WHERE id=$1 AND status='active'`, in.PlanID).Scan(&versionID)
	} else {
		err = fmt.Errorf("plan_id required")
	}
	if err != nil || !resourceUUID.MatchString(versionID) {
		s.writeErr(w, 400, "active plan required")
		return
	}
	if err := s.DB.Pool.QueryRow(r.Context(), `SELECT default_validity_days,timezone FROM plan_versions WHERE id=$1`, versionID).Scan(&days, &tz); err != nil {
		s.writeErr(w, 404, "plan version not found")
		return
	}
	expires, err := parseWhen(in.ExpiresAt, starts.AddDate(0, 0, days))
	if err != nil || !expires.After(starts) {
		s.writeErr(w, 400, "expires_at must be after starts_at")
		return
	}
	daily, err := moneyArg(in.DailyLimitUSD)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	weekly, err := moneyArg(in.WeeklyLimitUSD)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	monthly, err := moneyArg(in.MonthlyLimitUSD)
	if err != nil {
		s.writeErr(w, 400, err.Error())
		return
	}
	if in.ConcurrencyOverride != nil && *in.ConcurrencyOverride <= 0 || in.MaxKeysOverride != nil && *in.MaxKeysOverride <= 0 {
		s.writeErr(w, 400, "overrides must be positive")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO user_subscriptions(member_id,plan_version_id,status,starts_at,expires_at,daily_limit_override,weekly_limit_override,monthly_limit_override,concurrency_override,max_keys_override,allowed_models_override,assigned_by,notes) VALUES($1,$2,CASE WHEN $3>now() THEN 'scheduled' ELSE 'active' END,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id::text`, in.MemberID, versionID, starts, expires, daily, weekly, monthly, in.ConcurrencyOverride, in.MaxKeysOverride, in.AllowedModelsOverride, actorFrom(r), in.Notes).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	if err := s.addSubscriptionPolicies(r, tx, id, versionID, daily, weekly, monthly, tz, actorFrom(r)); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "subscription.assign", "subscription", id, map[string]any{"member_id": in.MemberID, "plan_version_id": versionID}, "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) addSubscriptionPolicies(r *http.Request, tx pgx.Tx, subID, versionID string, daily, weekly, monthly any, tz, actor string) error {
	var baseDaily, baseWeekly, baseMonthly *string
	if err := tx.QueryRow(r.Context(), `SELECT daily_limit_usd::text,weekly_limit_usd::text,monthly_limit_usd::text FROM plan_versions WHERE id=$1`, versionID).Scan(&baseDaily, &baseWeekly, &baseMonthly); err != nil {
		return err
	}
	values := []struct {
		period   string
		override any
		base     *string
	}{{"day", daily, baseDaily}, {"week", weekly, baseWeekly}, {"month", monthly, baseMonthly}}
	for _, v := range values {
		amount := v.override
		if amount == nil && v.base != nil {
			amount = *v.base
		}
		if amount == nil {
			continue
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO budget_policies(owner_type,owner_subscription_id,period,timezone,mode,amount,status,created_by) VALUES('subscription',$1,$2,$3,'fixed',$4,'active',$5)`, subID, v.period, tz, amount, actor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request, memberID string) {
	args := []any{}
	where := ""
	if memberID != "" {
		args = append(args, memberID)
		where = "WHERE us.member_id=$1"
	}
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT us.id::text,us.member_id::text,m.name,p.id::text,p.name,pv.id::text,pv.version_number,us.status,us.starts_at::text,us.expires_at::text,
		COALESCE(us.concurrency_override,pv.concurrency_limit),COALESCE(us.max_keys_override,pv.max_keys),
		COALESCE(COALESCE(us.daily_limit_override,pv.daily_limit_usd)::text,''),COALESCE(COALESCE(us.weekly_limit_override,pv.weekly_limit_usd)::text,''),COALESCE(COALESCE(us.monthly_limit_override,pv.monthly_limit_usd)::text,''),
		COALESCE(us.allowed_models_override,pv.allowed_models),pv.rate_multiplier::text,us.version,us.notes
		FROM user_subscriptions us JOIN members m ON m.id=us.member_id JOIN plan_versions pv ON pv.id=us.plan_version_id JOIN plans p ON p.id=pv.plan_id `+where+` ORDER BY us.expires_at DESC`, args...)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, mid, memberName, pid, pname, vid, status, starts, expires, daily, weekly, monthly, rateMultiplier, notes string
		var pv, conc, max, version int
		var models []string
		if err := rows.Scan(&id, &mid, &memberName, &pid, &pname, &vid, &pv, &status, &starts, &expires, &conc, &max, &daily, &weekly, &monthly, &models, &rateMultiplier, &version, &notes); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "member_id": mid, "member_name": memberName, "plan_id": pid, "plan_name": pname, "plan_version_id": vid, "plan_version": pv, "status": availability(status, starts, expires), "starts_at": starts, "expires_at": expires, "concurrency_limit": conc, "max_keys": max, "daily_limit_usd": daily, "weekly_limit_usd": weekly, "monthly_limit_usd": monthly, "rate_multiplier": rateMultiplier, "allowed_models": models, "version": version, "notes": notes})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func availability(status, starts, expires string) string {
	now := time.Now()
	st := parseDBTime(starts)
	ex := parseDBTime(expires)
	if status == "revoked" || status == "suspended" {
		return status
	}
	if !st.IsZero() && now.Before(st) {
		return "scheduled"
	}
	if !ex.IsZero() && now.After(ex) {
		return "expired"
	}
	return "active"
}

func parseDBTime(v string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05-07", "2006-01-02 15:04:05.999999999Z07", "2006-01-02 15:04:05Z07"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *Server) patchSubscription(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Status    *string `json:"status"`
		ExpiresAt *string `json:"expires_at"`
		Notes     *string `json:"notes"`
		Version   int     `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "suspended" && *req.Status != "revoked" {
		s.writeErr(w, 400, "status must be active, suspended or revoked")
		return
	}
	var expiry any
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			s.writeErr(w, 400, "invalid expires_at")
			return
		}
		expiry = t
	}
	tag, err := s.DB.Pool.Exec(r.Context(), `UPDATE user_subscriptions SET status=COALESCE($2,status),expires_at=COALESCE($3,expires_at),notes=COALESCE($4,notes),version=version+1,updated_at=now() WHERE id=$1 AND version=$5`, id, req.Status, expiry, req.Notes, req.Version)
	if err != nil || tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "version conflict or subscription not found")
		return
	}
	s.notifyMutation()
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	identity := identityFrom(r)
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.NewPassword) == "" {
		s.writeErr(w, 400, "old_password and new_password required")
		return
	}
	var hash string
	if err := s.DB.Pool.QueryRow(r.Context(), `SELECT password_hash FROM members WHERE id=$1`, identity.MemberID).Scan(&hash); err != nil || !authPasswordMatches(hash, req.OldPassword) {
		s.writeErr(w, 403, "current password is incorrect")
		return
	}
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if _, err := s.DB.Pool.Exec(r.Context(), `UPDATE members SET password_hash=$2,version=version+1,updated_at=now() WHERE id=$1`, identity.MemberID, newHash); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	_ = s.Auth.RevokeOtherMemberSessions(r.Context(), r, identity.MemberID)
	s.DB.LogAdminEvent(r.Context(), identity.MemberID, "member.password_change", "member", identity.MemberID, nil, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}

func authPasswordMatches(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

type effectiveSubscription struct {
	ID          string
	MemberID    string
	Status      string
	StartsAt    time.Time
	ExpiresAt   time.Time
	Concurrency int
	MaxKeys     int
	Models      []string
}

func (s *Server) subscriptionForMember(r *http.Request, memberID, subscriptionID string) (effectiveSubscription, error) {
	var sub effectiveSubscription
	err := s.DB.Pool.QueryRow(r.Context(), `
		SELECT us.id::text,us.member_id::text,us.status,us.starts_at,us.expires_at,
		       COALESCE(us.concurrency_override,pv.concurrency_limit),COALESCE(us.max_keys_override,pv.max_keys),
		       COALESCE(us.allowed_models_override,pv.allowed_models)
		FROM user_subscriptions us JOIN plan_versions pv ON pv.id=us.plan_version_id
		WHERE us.id=$1 AND us.member_id=$2`, subscriptionID, memberID).
		Scan(&sub.ID, &sub.MemberID, &sub.Status, &sub.StartsAt, &sub.ExpiresAt, &sub.Concurrency, &sub.MaxKeys, &sub.Models)
	if err != nil {
		return sub, err
	}
	if sub.Status != "active" || time.Now().Before(sub.StartsAt) || !time.Now().Before(sub.ExpiresAt) {
		return sub, fmt.Errorf("subscription is not active")
	}
	return sub, nil
}

type ownKeyInput struct {
	SubscriptionID string   `json:"subscription_id"`
	Name           string   `json:"name"`
	ExpiresAt      string   `json:"expires_at"`
	Concurrency    *int     `json:"concurrency_limit"`
	AllowedModels  []string `json:"allowed_models"`
}

func subset(requested, allowed []string) bool {
	if allowed == nil {
		return true
	}
	for _, candidate := range requested {
		found := false
		for _, permitted := range allowed {
			if candidate == permitted {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *Server) createOwnKey(w http.ResponseWriter, r *http.Request) {
	identity := identityFrom(r)
	var in ownKeyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" || !resourceUUID.MatchString(in.SubscriptionID) {
		s.writeErr(w, 400, "subscription_id and name required")
		return
	}
	sub, err := s.subscriptionForMember(r, identity.MemberID, in.SubscriptionID)
	if err != nil {
		s.writeErr(w, 403, "subscription is not available")
		return
	}
	limit := 1
	if in.Concurrency != nil {
		limit = *in.Concurrency
	}
	if limit <= 0 || limit > sub.Concurrency {
		s.writeErr(w, 400, "concurrency_limit exceeds subscription")
		return
	}
	if !subset(in.AllowedModels, sub.Models) {
		s.writeErr(w, 400, "allowed_models exceeds subscription")
		return
	}
	var expiry any
	if strings.TrimSpace(in.ExpiresAt) != "" {
		t, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil || t.After(sub.ExpiresAt) {
			s.writeErr(w, 400, "expires_at must not exceed subscription expiry")
			return
		}
		expiry = t
	}
	var count int
	if err := s.DB.Pool.QueryRow(r.Context(), `SELECT count(*) FROM api_keys WHERE user_subscription_id=$1 AND status <> 'revoked'`, sub.ID).Scan(&count); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	if count >= sub.MaxKeys {
		s.writeErr(w, 409, "subscription key limit reached")
		return
	}
	secret := "sk-subai-" + randToken(30)
	prefix := secret[:16]
	// An empty requested model list inherits the subscription's effective list.
	models := in.AllowedModels
	if len(models) == 0 && sub.Models != nil {
		models = sub.Models
	}
	var id string
	err = s.DB.Pool.QueryRow(r.Context(), `INSERT INTO api_keys(member_id,user_subscription_id,name,public_prefix,key_hash,expires_at,concurrency_limit,allowed_models) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`, identity.MemberID, sub.ID, in.Name, prefix, storage.HashToken(secret), expiry, limit, models).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), identity.MemberID, "key.create", "api_key", id, map[string]any{"subscription_id": sub.ID, "name": in.Name}, "")
	s.writeJSON(w, 201, map[string]any{"id": id, "key": secret, "public_prefix": prefix, "note": "store this key now; it is not retrievable later"})
}

func (s *Server) listOwnKeys(w http.ResponseWriter, r *http.Request) {
	identity := identityFrom(r)
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT k.id::text,k.name,k.public_prefix,k.status,COALESCE(k.expires_at::text,''),k.concurrency_limit,k.allowed_models,k.version,k.created_at::text,COALESCE(k.last_used_at::text,''),
		       COALESCE(us.id::text,''),COALESCE(p.name,'Legacy')
		FROM api_keys k LEFT JOIN user_subscriptions us ON us.id=k.user_subscription_id
		LEFT JOIN plan_versions pv ON pv.id=us.plan_version_id LEFT JOIN plans p ON p.id=pv.plan_id
		WHERE k.member_id=$1 ORDER BY k.created_at DESC`, identity.MemberID)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, prefix, status, expiry, created, last, subID, plan string
		var conc, version int
		var models []string
		if err := rows.Scan(&id, &name, &prefix, &status, &expiry, &conc, &models, &version, &created, &last, &subID, &plan); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "public_prefix": prefix, "status": status, "expires_at": expiry, "concurrency_limit": conc, "allowed_models": models, "version": version, "created_at": created, "last_used_at": last, "subscription_id": subID, "plan_name": plan})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) ownKey(w http.ResponseWriter, r *http.Request, id string) {
	identity := identityFrom(r)
	var exists bool
	if err := s.DB.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM api_keys WHERE id=$1 AND member_id=$2)`, id, identity.MemberID).Scan(&exists); err != nil || !exists {
		s.writeErr(w, 404, "key not found")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/revoke") {
		if r.Method != http.MethodPost {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.revokeKey(w, r, id)
		return
	}
	if r.Method != http.MethodPatch {
		s.writeErr(w, 405, "method not allowed")
		return
	}
	s.patchKey(w, r, id)
}

func (s *Server) listUserGrants(w http.ResponseWriter, r *http.Request, memberID, kind string) {
	var query string
	if kind == "account" {
		query = `SELECT g.id::text,a.id::text,a.label,g.priority,g.status,g.starts_at::text,COALESCE(g.expires_at::text,''),g.notes,g.version FROM user_account_grants g JOIN accounts a ON a.id=g.account_id WHERE g.member_id=$1 ORDER BY g.priority,a.label`
	} else {
		query = `SELECT g.id::text,p.id::text,p.name,g.priority,g.status,g.starts_at::text,COALESCE(g.expires_at::text,''),g.notes,g.version FROM user_pool_grants g JOIN account_groups p ON p.id=g.pool_id WHERE g.member_id=$1 ORDER BY g.priority,p.name`
	}
	rows, err := s.DB.Pool.Query(r.Context(), query, memberID)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, targetID, targetName, status, starts, expires, notes string
		var priority, version int
		if err := rows.Scan(&id, &targetID, &targetName, &priority, &status, &starts, &expires, &notes, &version); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "target_id": targetID, "target_name": targetName, "priority": priority, "status": status, "starts_at": starts, "expires_at": expires, "notes": notes, "version": version})
	}
	s.writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createUserGrant(w http.ResponseWriter, r *http.Request, memberID, kind string) {
	var req struct {
		TargetID  string `json:"target_id"`
		Priority  int    `json:"priority"`
		StartsAt  string `json:"starts_at"`
		ExpiresAt string `json:"expires_at"`
		Notes     string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !resourceUUID.MatchString(req.TargetID) {
		s.writeErr(w, 400, "valid target_id required")
		return
	}
	starts, err := parseWhen(req.StartsAt, time.Now().UTC())
	if err != nil {
		s.writeErr(w, 400, "invalid starts_at")
		return
	}
	var expires any
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil || !t.After(starts) {
			s.writeErr(w, 400, "invalid expires_at")
			return
		}
		expires = t
	}
	if req.Priority == 0 {
		req.Priority = 50
	}
	table, column := "user_account_grants", "account_id"
	if kind == "pool" {
		table, column = "user_pool_grants", "pool_id"
	}
	var id string
	err = s.DB.Pool.QueryRow(r.Context(), `INSERT INTO `+table+`(member_id,`+column+`,priority,starts_at,expires_at,assigned_by,notes) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, memberID, req.TargetID, req.Priority, starts, expires, actorFrom(r), req.Notes).Scan(&id)
	if err != nil {
		s.writeErr(w, 409, err.Error())
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "user."+kind+"_grant.create", kind+"_grant", id, map[string]any{"member_id": memberID, "target_id": req.TargetID}, "")
	s.writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) patchUserGrant(w http.ResponseWriter, r *http.Request, memberID, kind, grantID string) {
	var req struct {
		Status  *string `json:"status"`
		Version int     `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || expectVersion(req.Version) != nil {
		s.writeErr(w, 400, "version required")
		return
	}
	if req.Status == nil || (*req.Status != "active" && *req.Status != "disabled" && *req.Status != "revoked") {
		s.writeErr(w, 400, "status must be active, disabled or revoked")
		return
	}
	table := "user_account_grants"
	if kind == "pool" {
		table = "user_pool_grants"
	}
	tag, err := s.DB.Pool.Exec(r.Context(), `UPDATE `+table+` SET status=$3,version=version+1,updated_at=now() WHERE id=$1 AND member_id=$2 AND version=$4`, grantID, memberID, *req.Status, req.Version)
	if err != nil || tag.RowsAffected() == 0 {
		s.writeErr(w, 409, "version conflict or grant not found")
		return
	}
	s.notifyMutation()
	s.DB.LogAdminEvent(r.Context(), actorFrom(r), "user."+kind+"_grant.update", kind+"_grant", grantID, map[string]any{"member_id": memberID, "status": *req.Status}, "")
	s.writeJSON(w, 200, map[string]bool{"ok": true})
}
