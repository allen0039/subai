package admin

import (
	"net/http"
	"strings"
	"time"
)

// dashboard returns a bounded operational snapshot. Historical charts must
// consume this endpoint instead of loading and aggregating every request in the
// browser.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "24h"
	}
	var start time.Time
	var trunc string
	switch rangeName {
	case "24h":
		start, trunc = now.Add(-24*time.Hour), "hour"
	case "7d":
		start, trunc = now.AddDate(0, 0, -7), "day"
	default:
		s.writeErr(w, http.StatusBadRequest, "range must be 24h or 7d")
		return
	}

	ctx := r.Context()
	var activeRequests, unknownRequests int
	if err := s.DB.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state NOT IN ('completed','failed_after_dispatch','rejected','audit_failed','cancelled_before_dispatch','failed_before_dispatch','unknown')),
		       count(*) FILTER (WHERE state='unknown')
		FROM requests`).Scan(&activeRequests, &unknownRequests); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}

	type bucket struct {
		Start     string `json:"start"`
		Requests  int    `json:"requests"`
		Succeeded int    `json:"succeeded"`
		Failed    int    `json:"failed"`
		Cost      string `json:"cost"`
	}
	rows, err := s.DB.Pool.Query(ctx, `
		WITH request_buckets AS (
			SELECT date_trunc($1, created_at) AS bucket,
			       count(*) AS requests,
			       count(*) FILTER (WHERE state='completed') AS succeeded,
			       count(*) FILTER (WHERE state IN ('failed_after_dispatch','rejected','audit_failed','cancelled_before_dispatch','failed_before_dispatch','unknown')) AS failed
			FROM requests WHERE created_at >= $2 GROUP BY 1
		), ledger_buckets AS (
			SELECT date_trunc($1, created_at) AS bucket, COALESCE(sum(cost), 0)::text AS cost
			FROM usage_ledger WHERE created_at >= $2 GROUP BY 1
		)
		SELECT r.bucket::text, r.requests, r.succeeded, r.failed, COALESCE(l.cost, '0')
		FROM request_buckets r LEFT JOIN ledger_buckets l USING (bucket)
		ORDER BY r.bucket`, trunc, start)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	buckets := []bucket{}
	for rows.Next() {
		var item bucket
		if err := rows.Scan(&item.Start, &item.Requests, &item.Succeeded, &item.Failed, &item.Cost); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		buckets = append(buckets, item)
	}
	if err := rows.Err(); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}

	accountRows, err := s.DB.Pool.Query(ctx, `SELECT state, count(*) FROM accounts GROUP BY state ORDER BY state`)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer accountRows.Close()
	accounts := []map[string]any{}
	for accountRows.Next() {
		var state string
		var count int
		if err := accountRows.Scan(&state, &count); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		accounts = append(accounts, map[string]any{"state": state, "count": count})
	}
	if err := accountRows.Err(); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}

	var quotaCoverage, quotaStale int
	if err := s.DB.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE snapshot IS NOT NULL),
		       count(*) FILTER (WHERE snapshot IS NOT NULL AND fetched_at < now() - interval '15 minutes')
		FROM account_quota_snapshots`).Scan(&quotaCoverage, &quotaStale); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	ready, reasons := s.Ready.Check(ctx)
	s.writeJSON(w, 200, map[string]any{
		"as_of": now.Format(time.RFC3339), "range": rangeName, "bucket": trunc,
		"production_ready": ready, "not_ready_reasons": reasons,
		"active_requests": activeRequests, "unknown_requests": unknownRequests,
		"accounts": accounts,
		"quota":    map[string]any{"coverage": quotaCoverage, "stale": quotaStale, "stale_after_seconds": 900},
		"series":   buckets,
	})
}

// search performs a small, prefix-only search across administrator-visible
// resources. The result DTO intentionally excludes all credential fields.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		s.writeJSON(w, 200, map[string]any{"data": []any{}})
		return
	}
	if len([]rune(q)) < 2 || len([]rune(q)) > 80 {
		s.writeErr(w, http.StatusBadRequest, "q must be 2 to 80 characters")
		return
	}
	types := map[string]bool{}
	requested := strings.TrimSpace(r.URL.Query().Get("types"))
	if requested == "" {
		requested = "user,key,account,pool"
	}
	for _, value := range strings.Split(requested, ",") {
		value = strings.TrimSpace(value)
		if value != "user" && value != "key" && value != "account" && value != "pool" {
			s.writeErr(w, http.StatusBadRequest, "invalid search type")
			return
		}
		types[value] = true
	}
	pattern := q + "%"
	data := []map[string]any{}
	ctx := r.Context()
	appendRows := func(kind, href, query string) error {
		rows, err := s.DB.Pool.Query(ctx, query, pattern)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, title, status string
			if err := rows.Scan(&id, &title, &status); err != nil {
				return err
			}
			data = append(data, map[string]any{"type": kind, "id": id, "title": title, "status": status, "href": href})
		}
		return rows.Err()
	}
	if types["user"] {
		if err := appendRows("user", "#/users", `SELECT id::text, name, status FROM members WHERE name ILIKE $1 ORDER BY name LIMIT 5`); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
	}
	if types["key"] {
		if err := appendRows("key", "#/keys", `SELECT id::text, name || ' · ' || public_prefix, status FROM api_keys WHERE name ILIKE $1 OR public_prefix ILIKE $1 ORDER BY name LIMIT 5`); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
	}
	if types["account"] {
		if err := appendRows("account", "#/accounts", `SELECT id::text, label, state FROM accounts WHERE label ILIKE $1 ORDER BY label LIMIT 5`); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
	}
	if types["pool"] {
		if err := appendRows("pool", "#/pools", `SELECT id::text, name, status FROM account_groups WHERE name ILIKE $1 ORDER BY name LIMIT 5`); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
	}
	s.writeJSON(w, 200, map[string]any{"data": data})
}

// listRecentEvents supplies a bounded incremental event feed. Audit summaries
// are already sanitized before storage; raw requests and credentials are never
// selected here.
func (s *Server) listRecentEvents(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil || parsed.Before(time.Now().UTC().Add(-7*24*time.Hour)) || parsed.After(time.Now().UTC().Add(time.Minute)) {
			s.writeErr(w, http.StatusBadRequest, "since must be an RFC3339 time within the last 7 days")
			return
		}
		since = parsed.UTC()
	}
	rows, err := s.DB.Pool.Query(r.Context(), `
		SELECT created_at::text, kind, id, title, description FROM (
			SELECT created_at, 'admin'::text AS kind, id::text AS id, action AS title,
			       trim(concat_ws(' ', target_type, target_id)) AS description FROM admin_events
			UNION ALL
			SELECT created_at, 'audit'::text AS kind, id::text AS id, decision AS title, summary AS description FROM audit_events
		) events WHERE created_at > $1 ORDER BY created_at DESC LIMIT 50`, since)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var created, kind, id, title, description string
		if err := rows.Scan(&created, &kind, &id, &title, &description); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{"created_at": created, "kind": kind, "id": id, "title": title, "description": description})
	}
	if err := rows.Err(); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]any{"data": out, "since": since.Format(time.RFC3339)})
}

// memberOverview returns only the current member's subscription/key/use summary.
// It deliberately describes resource access abstractly rather than leaking
// upstream account, proxy or pool identities.
func (s *Server) memberOverview(w http.ResponseWriter, r *http.Request) {
	memberID := identityFrom(r).MemberID
	ctx := r.Context()
	var activeSubscriptions, activeKeys int
	var lastUsed string
	if err := s.DB.Pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM user_subscriptions WHERE member_id=$1 AND status='active' AND starts_at <= now() AND expires_at > now()),
		       (SELECT count(*) FROM api_keys WHERE member_id=$1 AND status='active' AND (expires_at IS NULL OR expires_at > now())),
		       COALESCE((SELECT max(last_used_at)::text FROM api_keys WHERE member_id=$1), '')`, memberID).Scan(&activeSubscriptions, &activeKeys, &lastUsed); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT date_trunc('day', l.created_at)::text, COALESCE(sum(l.cost), 0)::text
		FROM usage_ledger l JOIN api_keys k ON k.id=l.api_key_id
		WHERE k.member_id=$1 AND l.created_at >= date_trunc('day', now()) - interval '6 days'
		GROUP BY 1 ORDER BY 1`, memberID)
	if err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	series := []map[string]string{}
	for rows.Next() {
		var date, cost string
		if err := rows.Scan(&date, &cost); err != nil {
			s.writeErr(w, 500, err.Error())
			return
		}
		series = append(series, map[string]string{"date": date, "cost": cost})
	}
	if err := rows.Err(); err != nil {
		s.writeErr(w, 500, err.Error())
		return
	}
	s.writeJSON(w, 200, map[string]any{
		"active_subscriptions": activeSubscriptions, "active_keys": activeKeys, "last_used_at": lastUsed,
		"resource_access": "managed_by_subscription", "series": series,
	})
}
