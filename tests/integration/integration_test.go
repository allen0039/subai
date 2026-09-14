// Package integration runs the gateway end to end against a real PostgreSQL
// and synthetic upstream / moderation services (plan §23: 自动测试主要通过
// 模拟服务确定性执行). Set TEST_DATABASE_URL to enable; tests are skipped
// otherwise. Tests never touch real upstream or real moderation APIs.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"subai/internal/accounts"
	"subai/internal/admin"
	"subai/internal/audit"
	"subai/internal/auth"
	"subai/internal/billing"
	"subai/internal/config"
	"subai/internal/egress"
	"subai/internal/gateway"
	"subai/internal/scheduler"
	"subai/internal/storage"
	"subai/rules"
)

var masterKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestMain keeps process-level setup; the skip decision is per-test so
// `go test -v` shows explicit SKIP lines instead of a green non-run.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// requireDB makes the skip condition per-test visible in -v output
// (review round-2: 不再以 TestMain 静默退出冒充执行).
func requireDB(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; integration test SKIPPED (needs dockerized PostgreSQL)")
	}
}

type mockUpstream struct {
	mu       sync.Mutex
	requests int32
	srv      *httptest.Server
	usage    gateway.Usage
}

func newMockUpstream(t *testing.T, usage gateway.Usage) *mockUpstream {
	m := &mockUpstream{usage: usage}
	mux := http.NewServeMux()
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.requests, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"response\":{\"id\":\"resp_1\"}}\n\n")
		fmt.Fprintf(w, "event: response.completed\ndata: {\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":%d,\"output_tokens\":%d,\"input_tokens_details\":{\"cached_tokens\":%d}}}}\n\n",
			usage.InputTokens+usage.CachedTokens, usage.OutputTokens, usage.CachedTokens)
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockUpstream) count() int { return int(atomic.LoadInt32(&m.requests)) }

type mockModeration struct {
	mu         sync.Mutex
	requests   int32
	flagged    bool
	failMode   string // "", "timeout", "429", "500", "invalid_json"
	badBody    string // overrides the response body when set (review P1-8)
	srv        *httptest.Server
	flagSignal chan struct{}
}

func newMockModeration(t *testing.T, flagged bool) *mockModeration {
	m := &mockModeration{flagged: flagged}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.requests, 1)
		switch m.failMode {
		case "timeout":
			time.Sleep(2 * time.Second)
		case "429":
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		case "500":
			w.WriteHeader(500)
			return
		case "invalid_json":
			fmt.Fprint(w, "{not json")
			return
		}
		if m.badBody != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(m.badBody))
			return
		}
		decision := m.flagged
		if decision && m.flagSignal != nil {
			select {
			case <-m.flagSignal:
			default:
			}
		}
		body, _ := json.Marshal(map[string]any{
			"id": "modr-1", "model": "omni-moderation-latest",
			"results": []map[string]any{{"flagged": decision, "categories": map[string]bool{"violence": decision},
				"category_scores": map[string]float64{"violence": boolScore(decision)}}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func boolScore(b bool) float64 {
	if b {
		return 0.98
	}
	return 0.01
}

func (m *mockModeration) count() int { return int(atomic.LoadInt32(&m.requests)) }

// testEnv is a fully wired gateway + admin stack on a fresh schema.
type testEnv struct {
	db      *storage.DB
	gw      *gateway.Server
	admin   *admin.Server
	up      *mockUpstream
	moder   *mockModeration
	keyFull string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	requireDB(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// Fresh schema per test: drop + migrate.
	// R5-03: Include account_holds in cleanup list
	for _, table := range []string{
		"admin_sessions", "oauth_sessions", "audit_reviews", "audit_events", "audit_policies", "audit_rules",
		"usage_ledger", "reservations", "requests", "model_prices", "price_versions",
		"plan_pool_bindings", "user_account_grants", "user_pool_grants", "user_subscriptions", "plan_versions", "plans",
		"budget_periods", "budget_policies", "key_routes", "account_group_members", "account_groups",
		"account_holds", "account_quota_snapshots", "accounts", "egress_policies", "proxy_profiles", "api_keys", "clients", "members", "admin_events", "settings", "schema_migrations",
	} {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table+" CASCADE"); err != nil {
			t.Fatalf("clean table %s: %v", table, err)
		}
	}
	db, err := storage.Open(ctx, os.Getenv("TEST_DATABASE_URL"), masterKey)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx, "../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	up := newMockUpstream(t, gateway.Usage{InputTokens: 90, CachedTokens: 10, OutputTokens: 50})
	moder := newMockModeration(t, false)

	cfg := &config.Config{
		ListenAddr: ":0", MasterKey: masterKey,
		ProductionReady: true, AllowSyntheticPrices: true, // explicit test mode (review P1-2)
		PerKeyConcurrency: 1, PerAccountConcurrency: 1,
		RequestBodyLimit: 8 << 20, AuditBodyMemoryLimit: 128 << 20,
		AuditCacheTTL: 600 * time.Second, AuditWaitQueueMax: 50, AuditMaxInFlight: 4, AuditPerKeyQueue: 10,
		AuditWaitTimeout: 5 * time.Second, AuditCallTimeout: 2 * time.Second,
		AuditTotalBudget: 8 * time.Second, AuditRetries: 2,
		ModerationEndpoint: moder.srv.URL, ModerationModel: "omni-moderation-latest",
		ModerationAPIKey: "test-platform-key",
		UpstreamTimeout:  30 * time.Second,
		UpstreamBaseURL:  up.srv.URL + "/responses",
		OutputBound:      100,
		BillingMode:      "strict_reservation",
	}
	cfg.UpstreamBaseURL = strings.TrimSuffix(cfg.UpstreamBaseURL, "/responses")

	if err := audit.SeedDefaults(ctx, db.Pool, rules.Defaults()); err != nil {
		t.Fatalf("seed rules: %v", err)
	}
	ruleset, err := audit.LoadRuleset(ctx, db.Pool)
	if err != nil {
		t.Fatalf("load ruleset: %v", err)
	}
	engine := audit.NewEngine(ruleset)
	moderClient := &audit.ModerationClient{
		Endpoint: cfg.ModerationEndpoint, Model: cfg.ModerationModel, APIKey: cfg.ModerationAPIKey,
		HTTP: moder.srv.Client(), CallTimeout: cfg.AuditCallTimeout, Retries: 2, TotalBudget: cfg.AuditTotalBudget,
	}
	pipeline := audit.NewPipeline(engine, moderClient, audit.PipelineConfig{
		HMACKey: []byte("test-hmac"), WaitQueueMax: 50, MaxInFlight: 4, PerKeyQueue: 10,
		WaitTimeout: 5 * time.Second, TotalBudget: cfg.AuditTotalBudget, CallTimeout: cfg.AuditCallTimeout,
		Retries: 2, CacheTTL: cfg.AuditCacheTTL, ModerationModel: cfg.ModerationModel, Strict: true,
	})
	pipeline.ExceptionLookup = gateway.AuditExceptionLookup(db.Pool)

	authSvc := auth.NewService(db)
	st := gateway.NewStateTracker(db.Pool)
	slots := scheduler.NewSlots()
	sched := scheduler.New(pool, slots)
	sched.SetMaxPerAccount(cfg.PerAccountConcurrency)
	egressResolver := egress.NewResolver(db)
	readiness := gateway.NewReadiness(cfg, db)
	gwSrv := &gateway.Server{
		Cfg: cfg, DB: db, Auth: authSvc, State: st, Pipeline: pipeline,
		Sched: sched, Egress: egressResolver,
		Upstream: &gateway.Upstream{BaseURL: cfg.UpstreamBaseURL},
		Slots:    slots,
		Sticky:   gateway.NewStickyStore(time.Minute),
		Ready:    readiness,
		BodyMem:  gateway.NewBodyMemoryGate(cfg.AuditBodyMemoryLimit),
	}
	adminSrv := &admin.Server{DB: db, Auth: authSvc, Rules: engine, Ready: readiness}

	e := &testEnv{db: db, gw: gwSrv, admin: adminSrv, up: up, moder: moder}
	e.seed(ctx, t)
	return e
}

// seed creates member, key, account (direct egress), route, budget, prices.
func (e *testEnv) seed(ctx context.Context, t *testing.T) {
	t.Helper()
	var memberID, keyID, accountID, groupID string
	if err := e.db.Pool.QueryRow(ctx,
		`INSERT INTO members(name, role) VALUES('owner','admin') RETURNING id::text`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	e.keyFull = "sk-subai-" + strings.Repeat("k", 40)
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO api_keys(member_id, name, public_prefix, key_hash, concurrency_limit)
		VALUES($1,'test-key','sk-subai-',$2,1) RETURNING id::text`,
		memberID, storage.HashToken(e.keyFull)).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx,
		`INSERT INTO account_groups(name) VALUES('g1') RETURNING id::text`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	creds, _ := json.Marshal(map[string]string{"access_token": "up-token-1", "account_id": "acct-1"})
	sealed, err := e.db.Encrypt(creds)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO accounts(label, credentials_ciphertext, concurrency_limit, priority, state)
		VALUES('acctA',$1,1,100,'active') RETURNING id::text`, sealed).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO account_group_members(group_id, account_id) VALUES($1,$2)`, groupID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO key_routes(api_key_id, target_type, target_id, priority)
		VALUES($1,'group',$2,100)`, keyID, groupID); err != nil {
		t.Fatal(err)
	}
	// Weekly budget $100 fixed on the account.
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO budget_policies(owner_type, owner_account_id, period, timezone, mode, amount)
		VALUES('account',$1,'week','UTC','fixed',100)`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO budget_policies(owner_type, owner_key_id, period, timezone, mode, amount)
		VALUES('key',$1,'week','UTC','fixed',50)`, keyID); err != nil {
		t.Fatal(err)
	}
	// Active price version.
	var pvID string
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO price_versions(origin, status, activated_at) VALUES('synthetic','active',now()) RETURNING id::text`).Scan(&pvID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO model_prices(price_version_id, model, input_per_mtok, cached_input_per_mtok, output_per_mtok)
		VALUES($1,'gpt-5-codex',2,0.2,10)`, pvID); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) post(t *testing.T, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if path == "/v1/models" {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		e.gw.Models(rec, req)
	} else {
		e.gw.Responses(rec, req)
	}
	return rec
}

func body(valid bool, text string) string {
	if valid {
		return fmt.Sprintf(`{"model":"gpt-5-codex","input":%q}`, text)
	}
	return text
}

// T-P1-01: full happy path — auth, audit allow, dispatch, stream, settle.
func TestHappyPathSettlesLedger(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "please write a fibonacci function"))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("expected SSE passthrough, got: %.200s", rec.Body.String())
	}
	if e.up.count() != 1 {
		t.Fatalf("upstream calls = %d, want 1", e.up.count())
	}
	var cost, spent string
	var state string
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT state FROM requests`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("request state = %s", state)
	}
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT cost::text, (SELECT spent::text FROM budget_periods LIMIT 1) FROM usage_ledger WHERE entry_type='charge'`).Scan(&cost, &spent); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	// (90*2 + 10*0.2 + 50*10)/1e6 = 0.000682
	if cost != "0.000682000000" {
		t.Fatalf("cost = %s", cost)
	}
	if spent != "0.000682000000" {
		t.Fatalf("spent = %s", spent)
	}
}

// T-P2-01/P3-01: secret hit → zero moderation calls AND zero upstream calls;
// audit log contains masked sample only (§11 凭证用例).
func TestSecretBlockedZeroUpstreamZeroModeration(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	rec := e.post(t, "/v1/responses", e.keyFull, body(true,
		"my key is -----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7fKj8xZ1vN4mQ2pL9sW3eR6tY0uH5bC1dX8aS2fG4hJ7kL9zQ\nAQAB\n-----END RSA PRIVATE KEY-----"))
	if rec.Code != 400 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "audit_blocked") {
		t.Fatalf("expected audit_blocked: %s", rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream must see zero calls, got %d", e.up.count())
	}
	if e.moder.count() != 0 {
		t.Fatalf("moderation must see zero calls for secret hits, got %d", e.moder.count())
	}
	var summary string
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT summary FROM audit_events WHERE decision='review'`).Scan(&summary); err != nil {
		t.Fatalf("audit event missing: %v", err)
	}
	if strings.Contains(summary, "MIIEpAIBAAKCAQEA") {
		t.Fatalf("audit event leaks the secret: %s", summary)
	}
}

// T-P3-01: moderation flags → upstream zero calls, 400 audit_blocked.
func TestModerationFlagZeroUpstream(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	e.moder.flagged = true
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "some flagged content"))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "audit_blocked") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream must see zero calls, got %d", e.up.count())
	}
}

// T-P3-02: moderation unavailable (500/timeout/invalid json) → 503, zero upstream.
func TestModerationUnavailableZeroUpstream(t *testing.T) {
	requireDB(t)
	for _, mode := range []string{"500", "invalid_json", "429"} {
		t.Run(mode, func(t *testing.T) {
			e := newTestEnv(t)
			e.moder.failMode = mode
			rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
			if rec.Code != 503 {
				t.Fatalf("mode %s: status %d body %s", mode, rec.Code, rec.Body.String())
			}
			if e.up.count() != 0 {
				t.Fatalf("mode %s: upstream saw %d calls", mode, e.up.count())
			}
		})
	}
}

// T-P3-02: identical input hits the moderation cache on the second request.
func TestModerationCacheHit(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	e.post(t, "/v1/responses", e.keyFull, body(true, "cache me please"))
	e.post(t, "/v1/responses", e.keyFull, body(true, "cache me please"))
	if e.moder.count() != 1 {
		t.Fatalf("moderation calls = %d, want 1 (second must be cached)", e.moder.count())
	}
	if e.up.count() != 2 {
		t.Fatalf("upstream calls = %d, want 2", e.up.count())
	}
}

// T-P4-02: budget exceeded → 429 budget_exceeded, zero upstream.
func TestBudgetExceededZeroUpstream(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	// Drain the key budget ($50): over-reserve in one request by shrinking limit.
	if _, err := e.db.Pool.Exec(context.Background(),
		`UPDATE budget_policies SET amount=0.0000001 WHERE owner_type='key'`); err != nil {
		t.Fatal(err)
	}
	e.db.LogAdminEvent(context.Background(), "test", "budget.shrink", "", "", nil, "")
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "budget_exceeded") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream must see zero calls, got %d", e.up.count())
	}
}

// Sub2API-style metered mode checks that a period is not already exhausted,
// makes no estimated hold, and charges the real usage after the response.
func TestMeteredBillingChargesAfterUsageWithoutEstimatedHold(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	e.gw.Cfg.BillingMode = "metered"
	if _, err := e.db.Pool.Exec(context.Background(),
		`UPDATE budget_policies SET amount=0.0000001 WHERE owner_type='key'`); err != nil {
		t.Fatal(err)
	}
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var amount, mode, cost string
	if err := e.db.Pool.QueryRow(context.Background(), `
		SELECT r.amount::text,r.billing_mode,l.cost::text
		FROM reservations r JOIN usage_ledger l ON l.request_id=r.request_id
		WHERE l.entry_type='charge' ORDER BY r.created_at LIMIT 1`).Scan(&amount, &mode, &cost); err != nil {
		t.Fatal(err)
	}
	if amount != "0.000000000000" || mode != "metered" || cost != "0.000682000000" {
		t.Fatalf("amount=%s mode=%s cost=%s", amount, mode, cost)
	}
}

func TestMeteredBillingAppliesSubscriptionPlanMultiplier(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	e.gw.Cfg.BillingMode = "metered"
	ctx := context.Background()
	var memberID, keyID, groupID, planID, versionID, subscriptionID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM members LIMIT 1`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys LIMIT 1`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM account_groups LIMIT 1`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO plans(name,status) VALUES('metered-plan','active') RETURNING id::text`).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO plan_versions(plan_id,version_number,daily_limit_usd,weekly_limit_usd,monthly_limit_usd,rate_multiplier,concurrency_limit,max_keys,default_validity_days)
		VALUES($1,1,10,20,30,1.5,2,2,30) RETURNING id::text`, planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE plans SET current_version_id=$2 WHERE id=$1`, planID, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO plan_pool_bindings(plan_version_id,pool_id) VALUES($1,$2)`, versionID, groupID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO user_subscriptions(member_id,plan_version_id,status,starts_at,expires_at)
		VALUES($1,$2,'active',now()-interval '1 minute',now()+interval '1 day') RETURNING id::text`, memberID, versionID).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE api_keys SET user_subscription_id=$2 WHERE id=$1`, keyID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO budget_policies(owner_type,owner_subscription_id,period,timezone,mode,amount)
		VALUES('subscription',$1,'day','UTC','fixed',10)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	e.gw.Auth.InvalidateKeyCache()
	e.gw.Sched.InvalidateRoutes()

	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var baseCost, multiplier, actualCost, frozenMultiplier string
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT l.base_cost::text,l.rate_multiplier::text,l.cost::text,r.rate_multiplier::text
		FROM usage_ledger l JOIN requests r ON r.id=l.request_id
		WHERE l.entry_type='charge'`).Scan(&baseCost, &multiplier, &actualCost, &frozenMultiplier); err != nil {
		t.Fatal(err)
	}
	if baseCost != "0.000682000000" || multiplier != "1.500000000000" || actualCost != "0.001023000000" || frozenMultiplier != "1.500000000000" {
		t.Fatalf("base=%s multiplier=%s actual=%s frozen=%s", baseCost, multiplier, actualCost, frozenMultiplier)
	}
}

func TestPriceCatalogSyncActivatesNewModels(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gpt-new":{"input_cost_per_token":0.000002,"cache_read_input_token_cost":0.0000002,"output_cost_per_token":0.00001}}`))
	}))
	defer catalog.Close()
	result, err := billing.SyncPriceCatalog(context.Background(), e.db.Pool, catalog.Client(), catalog.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Activated || result.Models != 1 {
		t.Fatalf("unexpected sync result: %#v", result)
	}
	price, err := billing.ModelPriceFor(context.Background(), e.db.Pool, result.VersionID, "gpt-new")
	if err != nil {
		t.Fatal(err)
	}
	if price.InputPerMTok.String() != "2" || price.CachedInputPerMTok.String() != "0.2" || price.OutputPerMTok.String() != "10" {
		t.Fatalf("unexpected imported price: %#v", price)
	}
}

// T-P4-01: no budget covering the request → refuse (§18.2).
func TestNoBudgetRefused(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	if _, err := e.db.Pool.Exec(context.Background(), `DELETE FROM budget_policies`); err != nil {
		t.Fatal(err)
	}
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != 429 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream must see zero calls")
	}
}

func TestMeteredBillingAllowsUnlimitedSubscription(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	e.gw.Cfg.BillingMode = "metered"
	if _, err := e.db.Pool.Exec(context.Background(), `DELETE FROM budget_policies`); err != nil {
		t.Fatal(err)
	}
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var ledgerRows, reservationRows int
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM usage_ledger WHERE entry_type='charge'`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM reservations`).Scan(&reservationRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 || reservationRows != 0 {
		t.Fatalf("ledger rows=%d reservation rows=%d", ledgerRows, reservationRows)
	}
}

// T-P1-02: invalid key → 401 invalid_api_key, zero upstream.
func TestInvalidKey(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	rec := e.post(t, "/v1/responses", "sk-subai-wrong", body(true, "hello"))
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "invalid_api_key") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream calls must be zero")
	}
}

// T-P1-03: key without routes → 403 access_denied (§16.2).
func TestNoRouteDenied(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	if _, err := e.db.Pool.Exec(context.Background(), `DELETE FROM key_routes`); err != nil {
		t.Fatal(err)
	}
	rec := e.post(t, "/v1/responses", e.keyFull, body(true, "hello"))
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "access_denied") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if e.up.count() != 0 {
		t.Fatalf("upstream calls must be zero")
	}
}

// T-P6-01: startup recovery marks never-dispatched requests cancelled and
// possibly-dispatched ones unknown with reservations kept (§19).
func TestStartupRecovery(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	// Insert two stuck requests: one pre-dispatch, one post-dispatch.
	var keyID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{"audit_passed", "streaming"} {
		if _, err := e.db.Pool.Exec(ctx, `
			INSERT INTO requests(id, api_key_id, model, state) VALUES($3,$1,'gpt-5-codex',$2)`,
			keyID, state, fmt.Sprintf("req_recovery_%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	st := gateway.NewStateTracker(e.db.Pool)
	summary, err := st.OnStartupRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Cancelled != 1 || summary.Unknown != 1 {
		t.Fatalf("summary = %+v, want cancelled=1 unknown=1", summary)
	}
	var cancelled, unknown int
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM requests WHERE state='cancelled_before_dispatch'`).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM requests WHERE state='unknown'`).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 || unknown != 1 {
		t.Fatalf("cancelled=%d unknown=%d", cancelled, unknown)
	}
	// Review P1-4: funds and accounts must reconcile, not just request states.
	// Attach a reservation + account to the pre-dispatch request and re-verify.
}

// T-P4-03: concurrent settlements of the same request are idempotent (§18.3).
func TestSettleIdempotent(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	var memberID, keyID, accountID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM members`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM accounts`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	var pvID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM price_versions WHERE status='active'`).Scan(&pvID); err != nil {
		t.Fatal(err)
	}
	price, err := billing.ModelPriceFor(ctx, e.db.Pool, pvID, "gpt-5-codex")
	if err != nil {
		t.Fatal(err)
	}
	var keyID2 string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM api_keys`).Scan(&keyID2); err != nil {
		t.Fatal(err)
	}
	reqID := "req_test_idempotent"
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO requests(id, api_key_id, model, state) VALUES($1,$2,'gpt-5-codex','received')`, reqID, keyID2); err != nil {
		t.Fatal(err)
	}
	if _, err := billing.Reserve(ctx, e.db.Pool, memberID, keyID, accountID, "", reqID, billing.RoundUpReservation(price.Cost(billing.Usage{InputTokens: 1000, OutputTokens: 100})), time.Now()); err != nil {
		t.Fatal(err)
	}
	usage := billing.Usage{InputTokens: 90, CachedTokens: 10, OutputTokens: 50}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := billing.Settle(ctx, e.db.Pool, reqID, 0, keyID, accountID, usage, price, pvID)
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("settle %d: %v", i, err)
		}
	}
	var charges int
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM usage_ledger WHERE request_id=$1 AND entry_type='charge'`, reqID).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if charges != 1 {
		t.Fatalf("charge rows = %d, want exactly 1", charges)
	}
}

// T-P6-02: a sixth+ client is pure data (no code changes) — create client+key
// via admin API and call successfully (§1 动态主体).
func TestSixthClientDynamic(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	var memberID string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM members`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	// Six more keys across six clients.
	for i := 1; i <= 6; i++ {
		var clientID, keyID string
		if err := e.db.Pool.QueryRow(ctx, `
			INSERT INTO clients(member_id, name, type) VALUES($1,$2,'computer') RETURNING id::text`,
			memberID, fmt.Sprintf("client-%d", i)).Scan(&clientID); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("sk-subai-client-%d-key-0000000000000000", i)
		if err := e.db.Pool.QueryRow(ctx, `
			INSERT INTO api_keys(member_id, client_id, name, public_prefix, key_hash)
			VALUES($1,$2,$3,'sk-subai-',$4) RETURNING id::text`,
			memberID, clientID, fmt.Sprintf("key-%d", i), storage.HashToken(key)).Scan(&keyID); err != nil {
			t.Fatal(err)
		}
		var accountID string
		if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM accounts LIMIT 1`).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Pool.Exec(ctx,
			`INSERT INTO key_routes(api_key_id, target_type, target_id) VALUES($1,'account',$2)`, keyID, accountID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Pool.Exec(ctx, `
			INSERT INTO budget_policies(owner_type, owner_key_id, period, timezone, mode, amount)
			VALUES('key',$1,'week','UTC','fixed',10)`, keyID); err != nil {
			t.Fatal(err)
		}
		rec := e.post(t, "/v1/responses", key, body(true, fmt.Sprintf("hello from client %d", i)))
		if rec.Code != 200 {
			t.Fatalf("client %d: status %d body %s", i, rec.Code, rec.Body.String())
		}
	}
}

// T-OAuth: PKCE flow against a synthetic authorization server; state single use.
func TestOAuthPKCEFlow(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()
	var codeCh = make(chan string, 1)
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "authorization_code" {
			w.WriteHeader(400)
			return
		}
		if r.FormValue("code") != <-codeCh {
			w.WriteHeader(400)
			return
		}
		if r.FormValue("code_verifier") == "" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	mgr := accounts.NewManager(e.db, "http://auth.test/authorize", tokenSrv.URL, "client-1", "http://cb.test")
	_, state, _, _, err := mgr.StartSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	code := "authcode-123"
	codeCh <- code
	accountID, err := mgr.CompleteCallback(ctx, state, code)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if accountID == "" {
		t.Fatal("no account created")
	}
	// Single use: replaying the state must fail.
	if _, err := mgr.CompleteCallback(ctx, state, code); err == nil {
		t.Fatal("state reuse must fail (single-use guarantee)")
	}
}
