// Command server is the SubAI gateway entry point: migrations, singleton lock
// (with liveness watch), rule seeding, startup reconciliation, data plane and
// admin API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata" // load timezone database for containers

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
	"subai/internal/server"
	"subai/internal/storage"
	"subai/rules"
)

func main() {
	migrateOnly := false
	for _, a := range os.Args[1:] {
		if a == "--migrate-only" {
			migrateOnly = true
		}
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx := context.Background()

	db, err := storage.Open(ctx, cfg.DatabaseURL, cfg.MasterKey)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(ctx, "migrations"); err != nil {
		if _, statErr := os.Stat("migrations"); statErr != nil {
			log.Fatalf("migrations dir not found (run from repo root or set working dir): %v / %v", err, statErr)
		}
		log.Fatalf("migrate: %v", err)
	}

	// ── Singleton lock + liveness watch (D-007 + review P2-14) ──────────────
	lockConn, err := db.Pool.Acquire(ctx)
	if err != nil {
		log.Fatalf("acquire: %v", err)
	}
	defer lockConn.Release()
	var locked bool
	if err := lockConn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, cfg.SingletonKey).Scan(&locked); err != nil {
		log.Fatalf("singleton lock: %v", err)
	}
	if !locked {
		log.Fatalf("another SubAI instance holds the singleton lock; refusing to start (§14)")
	}
	var lockPID int
	if err := lockConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&lockPID); err != nil {
		log.Fatalf("singleton lock pid: %v", err)
	}
	go watchSingletonLock(ctx, lockConn, cfg.SingletonKey, lockPID)

	// ── Seeds: default rules + synthetic price version (first boot only) ────
	if err := audit.SeedDefaults(ctx, db.Pool, rules.Defaults()); err != nil {
		log.Fatalf("seed rules: %v", err)
	}
	if err := audit.SeedSyntheticPrice(ctx, db.Pool, map[string][3]string{
		// Synthetic seed (D-008): placeholder numbers, never official pricing.
		"gpt-5-codex":            {"2.000000000000", "0.200000000000", "10.000000000000"},
		"gpt-5":                  {"1.500000000000", "0.150000000000", "8.000000000000"},
		"omni-moderation-latest": {"0", "0", "0"},
	}); err != nil {
		log.Fatalf("seed prices: %v", err)
	}

	// Dev bootstrap: only when explicitly configured; no factory password (§22).
	if cfg.DevBootstrapAdmin != "" && strings.Contains(cfg.DevBootstrapAdmin, ":") {
		parts := strings.SplitN(cfg.DevBootstrapAdmin, ":", 2)
		var exists bool
		_ = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM members WHERE role='admin')`).Scan(&exists)
		if !exists {
			hash, _ := auth.HashPassword(parts[1])
			_, _ = db.Pool.Exec(ctx, `INSERT INTO members(name, role, password_hash) VALUES($1,'admin',$2)`, parts[0], hash)
			log.Printf("bootstrapped admin %q from SUBAI_DEV_BOOTSTRAP_ADMIN", parts[0])
		}
	}

	if migrateOnly {
		log.Printf("migrations + seeds applied; exiting (--migrate-only)")
		return
	}

	priceHTTP := &http.Client{Timeout: 30 * time.Second}
	syncPrices := func(syncCtx context.Context) (billing.PriceSyncResult, error) {
		return billing.SyncPriceCatalog(syncCtx, db.Pool, priceHTTP, cfg.PriceSourceURL, cfg.PriceHashURL)
	}
	if result, syncErr := syncPrices(ctx); syncErr != nil {
		log.Printf("price catalog startup sync failed; retaining last active version: %v", syncErr)
	} else {
		log.Printf("price catalog ready: models=%d activated=%v unchanged=%v", result.Models, result.Activated, result.Unchanged)
	}
	go startPriceSyncWorker(ctx, cfg.PriceSyncPeriod, syncPrices)

	// ── Startup reconciliation (§19 + review P1-4): funds & accounts first ──
	st := gateway.NewStateTracker(db.Pool)
	summary, err := st.OnStartupRecovery(ctx)
	if err != nil {
		log.Fatalf("startup recovery: %v", err)
	}
	if summary.Cancelled+summary.Completed+summary.Unknown > 0 {
		log.Printf("startup recovery: cancelled=%d completed_reconciled=%d unknown=%d",
			summary.Cancelled, summary.Completed, summary.Unknown)
	}

	// ── Audit pipeline ───────────────────────────────────────────────────────
	ruleset, err := audit.LoadRuleset(ctx, db.Pool)
	if err != nil {
		log.Fatalf("load rules: %v", err)
	}
	engine := audit.NewEngine(ruleset)
	moderHTTP, err := egressClientForModeration(cfg.ModerationProxy)
	if err != nil {
		log.Fatalf("moderation egress: %v", err)
	}
	moder := &audit.ModerationClient{
		Endpoint: cfg.ModerationEndpoint, Model: cfg.ModerationModel, APIKey: cfg.ModerationAPIKey,
		HTTP: moderHTTP, CallTimeout: cfg.AuditCallTimeout, Retries: cfg.AuditRetries, TotalBudget: cfg.AuditTotalBudget,
	}
	pipeline := audit.NewPipeline(engine, moder, audit.PipelineConfig{
		HMACKey:         []byte(cfg.SessionSecret),
		WaitQueueMax:    cfg.AuditWaitQueueMax,
		MaxInFlight:     cfg.AuditMaxInFlight,
		PerKeyQueue:     cfg.AuditPerKeyQueue,
		WaitTimeout:     cfg.AuditWaitTimeout,
		TotalBudget:     cfg.AuditTotalBudget,
		CallTimeout:     cfg.AuditCallTimeout,
		Retries:         cfg.AuditRetries,
		CacheTTL:        cfg.AuditCacheTTL,
		ModerationModel: cfg.ModerationModel,
		Strict:          true,
	})
	pipeline.ExceptionLookup = gateway.AuditExceptionLookup(db.Pool)
	pipeline.SetPolicyVersion(audit.LoadPolicyVersion(ctx, db.Pool))

	// P2-02: Start retention worker to prevent unbounded audit data growth
	audit.StartRetentionWorker(ctx, db.Pool, audit.DefaultRetentionConfig())

	// ── Collaborators ────────────────────────────────────────────────────────
	authSvc := auth.NewService(db)
	slots := scheduler.NewSlots()
	sched := scheduler.New(db.Pool, slots)
	sched.SetMaxPerAccount(cfg.PerAccountConcurrency)
	egressResolver := egress.NewResolver(db)
	oauth := accounts.NewManager(db,
		envDefault("SUBAI_OAUTH_AUTHORIZE_URL", accounts.DefaultAuthorizeURL),
		envDefault("SUBAI_OAUTH_TOKEN_URL", accounts.DefaultTokenURL),
		envDefault("SUBAI_OAUTH_CLIENT_ID", accounts.DefaultClientID),
		envDefault("SUBAI_OAUTH_REDIRECT_URI", accounts.DefaultRedirectURI))

	ready := gateway.NewReadiness(cfg, db)
	reload := func() {
		rs, err := audit.LoadRuleset(context.WithoutCancel(ctx), db.Pool)
		if err != nil {
			log.Printf("reload rules failed: %v", err)
			return
		}
		engine.Swap(rs)
		pipeline.Cache.Clear()
		pipeline.SetPolicyVersion(audit.LoadPolicyVersion(context.WithoutCancel(ctx), db.Pool))
		authSvc.InvalidateKeyCache() // review P2-10: admin writes take effect immediately
		sched.InvalidateRoutes()
	}

	gw := &gateway.Server{
		Cfg: cfg, DB: db, Auth: authSvc, State: st, Pipeline: pipeline,
		Sched: sched, Egress: egressResolver,
		Upstream: &gateway.Upstream{BaseURL: cfg.UpstreamBaseURL},
		Slots:    slots,
		Sticky:   gateway.NewStickyStore(30 * time.Minute),
		Ready:    ready,
		Refresh:  oauth,
		BodyMem:  gateway.NewBodyMemoryGate(cfg.AuditBodyMemoryLimit),
	}

	adminSrv := &admin.Server{
		DB: db, Auth: authSvc, OAuth: oauth, Quota: accounts.NewQuotaClient(), Egress: egressResolver, Rules: engine,
		Reloader:  reload,
		Ready:     ready, // same gate as the data plane (review P1-2)
		PriceSync: syncPrices,
	}

	handler := server.Build(server.Deps{DB: db, Gateway: gw, Admin: adminSrv})

	addr := cfg.ListenAddr
	ok, reasons := ready.Check(ctx)
	log.Printf("SubAI gateway listening on %s (data_plane_ready=%v)", addr, ok)
	for _, r := range reasons {
		log.Printf("readiness: %s", r)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Printf("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func startPriceSyncWorker(ctx context.Context, interval time.Duration, syncPrices func(context.Context) (billing.PriceSyncResult, error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			result, err := syncPrices(syncCtx)
			cancel()
			if err != nil {
				log.Printf("price catalog background sync failed: %v", err)
				continue
			}
			if result.Activated {
				log.Printf("price catalog activated: version=%s models=%d", result.VersionID, result.Models)
			}
		}
	}
}

// watchSingletonLock stops the process the moment the lock connection dies —
// otherwise a new instance could acquire the lock while the old one still
// serves with stale in-memory state (review P2-14).
func watchSingletonLock(ctx context.Context, conn *pgxpool.Conn, key int, pid int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var held bool
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err := conn.QueryRow(probeCtx,
			`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=$1::bigint AND pid=$2)`,
			key, pid).Scan(&held)
		cancel()
		if err != nil || !held {
			log.Fatalf("singleton advisory lock lost (conn_err=%v held=%v); stopping to protect single-active invariants", err, held)
		}
	}
}

func egressClientForModeration(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return &http.Client{Timeout: 15 * time.Second}, nil
	}
	return egress.ClientForProfile(egress.Profile{Kind: kindOf(proxyURL), Endpoint: proxyURL, Status: "active"}, 15*time.Second)
}

func kindOf(u string) string {
	if strings.HasPrefix(u, "socks5://") {
		return "socks5"
	}
	return "http"
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
