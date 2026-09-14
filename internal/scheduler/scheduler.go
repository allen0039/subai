// Package scheduler selects upstream accounts for a request and enforces the
// two-layer concurrency limit (per-Key and per-account, §1). Selection walks
// the key's routes in priority tiers (review P2-11), skips accounts already at
// their concurrency limit, prefers the least-loaded, and acquires the
// concurrency slots in the same step so the pick/race window is closed.
// Sticky binding keeps a session on its account when the upstream
// conversation state requires it (§19).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoRoute          = errors.New("no route for key")
	ErrNoHealthyAccount = errors.New("no healthy account available")
	ErrConcurrency      = errors.New("concurrency limit exceeded")
)

type Route struct {
	TargetType string // account|group
	TargetID   string
	Priority   int
}

type Account struct {
	ID                string
	Provider          string
	Label             string
	UpstreamBaseURL   string
	UpstreamModel     string // mapped model for the selected request; never client-visible
	State             string
	ConcurrencyLimit  int
	Priority          int
	EgressPolicyID    *string
	GroupID           string // group the route resolved through ("" for direct account routes)
	RouteWeight       int    // member weight when selected from a weighted account pool
	CredentialVersion int
}

// Slots is the process-local concurrency coordinator (D-007: single active
// instance holds it; the DB singleton lock guarantees exclusivity).
type Slots struct {
	mu              sync.Mutex
	perKey          map[string]int
	perSubscription map[string]int
	perAcct         map[string]int
}

func NewSlots() *Slots {
	return &Slots{perKey: map[string]int{}, perSubscription: map[string]int{}, perAcct: map[string]int{}}
}

// Acquire preserves the original two-layer API for legacy callers.
func (s *Slots) Acquire(keyID string, keyLimit int, accountID string, accountLimit int) bool {
	return s.AcquireWithSubscription(keyID, keyLimit, "", 0, accountID, accountLimit)
}

// AcquireWithSubscription takes slots for the Key, selected subscription and
// upstream account. A blank subscription ID denotes a legacy Key and skips
// the middle layer until compatibility migration is complete.
func (s *Slots) AcquireWithSubscription(keyID string, keyLimit int, subscriptionID string, subscriptionLimit int, accountID string, accountLimit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if keyLimit > 0 && s.perKey[keyID] >= keyLimit {
		return false
	}
	if subscriptionID != "" && subscriptionLimit > 0 && s.perSubscription[subscriptionID] >= subscriptionLimit {
		return false
	}
	if accountLimit > 0 && s.perAcct[accountID] >= accountLimit {
		return false
	}
	s.perKey[keyID]++
	if subscriptionID != "" {
		s.perSubscription[subscriptionID]++
	}
	s.perAcct[accountID]++
	return true
}

func (s *Slots) Release(keyID, subscriptionID, accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perKey[keyID] > 0 {
		s.perKey[keyID]--
	}
	if subscriptionID != "" && s.perSubscription[subscriptionID] > 0 {
		s.perSubscription[subscriptionID]--
	}
	if s.perAcct[accountID] > 0 {
		s.perAcct[accountID]--
	}
}

// Inflight reports current account occupancy (selection input + metrics).
func (s *Slots) Inflight(accountID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perAcct[accountID]
}

// Scheduler reads routes and accounts from the database with a short cache.
// Slots is injected at construction (review P2-11: 调度与占槽必须使用同一实例).
type Scheduler struct {
	pool          *pgxpool.Pool
	slots         *Slots
	maxPerAccount int
	mu            sync.Mutex
	rot           map[string]int // rotation counters per key
	ttl           time.Duration
	cache         map[string]cacheEntry
}

// SetMaxPerAccount applies a deployment-wide ceiling in addition to the
// account-specific limit. A zero value leaves account limits unchanged.
func (s *Scheduler) SetMaxPerAccount(limit int) {
	s.mu.Lock()
	s.maxPerAccount = limit
	s.mu.Unlock()
}

func (s *Scheduler) accountLimit(account *Account) int {
	s.mu.Lock()
	global := s.maxPerAccount
	s.mu.Unlock()
	if global <= 0 || (account.ConcurrencyLimit > 0 && account.ConcurrencyLimit <= global) {
		return account.ConcurrencyLimit
	}
	return global
}

type cacheEntry struct {
	routes []Route
	at     time.Time
}

func New(pool *pgxpool.Pool, slots *Slots) *Scheduler {
	if slots == nil {
		slots = NewSlots()
	}
	return &Scheduler{pool: pool, slots: slots, rot: map[string]int{}, ttl: 5 * time.Second, cache: map[string]cacheEntry{}}
}

// RoutesFor returns the key's routes ordered by priority; no route means the
// key may not call at all (§16.2 key_routes: 默认无路由即禁止调用).
func (s *Scheduler) RoutesFor(ctx context.Context, keyID string) ([]Route, error) {
	s.mu.Lock()
	if e, ok := s.cache[keyID]; ok && time.Since(e.at) < s.ttl {
		s.mu.Unlock()
		return e.routes, nil
	}
	s.mu.Unlock()
	rows, err := s.pool.Query(ctx, `
		SELECT target_type, target_id::text, priority FROM (
			-- New Keys: the selected subscription contributes its plan pools.
			SELECT 'group'::text AS target_type, b.pool_id AS target_id, b.priority
			FROM api_keys k JOIN user_subscriptions us ON us.id=k.user_subscription_id AND us.member_id=k.member_id
			JOIN plan_pool_bindings b ON b.plan_version_id=us.plan_version_id
			WHERE k.id=$1 AND us.status='active' AND us.starts_at<=now() AND us.expires_at>now()
			UNION ALL
			-- Explicit user grants apply to every current subscription Key.
			SELECT 'account'::text, g.account_id, g.priority
			FROM api_keys k JOIN user_account_grants g ON g.member_id=k.member_id
			WHERE k.id=$1 AND k.user_subscription_id IS NOT NULL AND g.status='active'
			  AND g.starts_at<=now() AND (g.expires_at IS NULL OR g.expires_at>now())
			UNION ALL
			SELECT 'group'::text, g.pool_id, g.priority
			FROM api_keys k JOIN user_pool_grants g ON g.member_id=k.member_id
			WHERE k.id=$1 AND k.user_subscription_id IS NOT NULL AND g.status='active'
			  AND g.starts_at<=now() AND (g.expires_at IS NULL OR g.expires_at>now())
			UNION ALL
			-- Legacy Keys keep their existing routes until data migration finishes.
			SELECT kr.target_type, kr.target_id, kr.priority
			FROM key_routes kr JOIN api_keys k ON k.id=kr.api_key_id
			WHERE kr.api_key_id=$1 AND k.user_subscription_id IS NULL
		) routes ORDER BY priority ASC, target_id`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []Route
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.TargetType, &r.TargetID, &r.Priority); err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}
	s.mu.Lock()
	s.cache[keyID] = cacheEntry{routes: routes, at: time.Now()}
	s.mu.Unlock()
	return routes, nil
}

func (s *Scheduler) InvalidateRoutes() {
	s.mu.Lock()
	s.cache = map[string]cacheEntry{}
	s.mu.Unlock()
}

type cand struct {
	acct   *Account
	tier   int
	order  int
	weight int
}

// AcquireAccount picks an account for the key and acquires both concurrency
// layers in one step. Accounts already at their concurrency limit are never
// selected; when the chosen tier is full the next route tier is tried, so a
// saturated account cannot reject a request another account could serve
// (review P2-11). Returns a release func (never nil).
// AcquireAccount preserves the legacy scheduler API for existing callers.
func (s *Scheduler) AcquireAccount(ctx context.Context, keyID string, keyLimit int, stickyID string) (*Account, func(), error) {
	return s.AcquireAccountWithSubscription(ctx, keyID, keyLimit, "", 0, stickyID)
}

// AcquireAccountWithSubscription adds a subscription-wide concurrency layer.
func (s *Scheduler) AcquireAccountWithSubscription(ctx context.Context, keyID string, keyLimit int, subscriptionID string, subscriptionLimit int, stickyID string) (*Account, func(), error) {
	return s.acquire(ctx, keyID, keyLimit, subscriptionID, subscriptionLimit, "", stickyID)
}

// AcquireModelRoute selects only accounts that can serve publicModel and
// returns the mapped upstream model. Empty capability sets intentionally keep
// legacy accounts compatible; a configured set is a strict allow-list.
func (s *Scheduler) AcquireModelRoute(ctx context.Context, keyID string, keyLimit int, subscriptionID string, subscriptionLimit int, publicModel, stickyID string) (*Account, func(), error) {
	return s.acquire(ctx, keyID, keyLimit, subscriptionID, subscriptionLimit, publicModel, stickyID)
}

func (s *Scheduler) acquire(ctx context.Context, keyID string, keyLimit int, subscriptionID string, subscriptionLimit int, publicModel, stickyID string) (*Account, func(), error) {
	noop := func() {}
	routes, err := s.RoutesFor(ctx, keyID)
	if err != nil {
		return nil, noop, err
	}
	if len(routes) == 0 {
		return nil, noop, ErrNoRoute
	}

	var cands []cand
	seen := map[string]bool{}
	order := 0
	addAcct := func(a *Account, tier int) {
		if seen[a.ID] {
			return // keep the earliest (highest-priority) occurrence
		}
		seen[a.ID] = true
		weight := a.RouteWeight
		if weight < 1 {
			weight = 1
		}
		cands = append(cands, cand{acct: a, tier: tier, order: order, weight: weight})
		order++
	}
	// R5-06: Use Route.Priority as tier, not loop index
	for _, r := range routes {
		tier := r.Priority
		switch r.TargetType {
		case "account":
			if a, err := s.loadAccount(ctx, r.TargetID, ""); err == nil && a.State == "active" {
				if publicModel != "" {
					upstreamModel, ok, serr := s.accountSupportsModel(ctx, a.ID, publicModel)
					if serr != nil || !ok {
						continue
					}
					a.UpstreamModel = upstreamModel
				}
				addAcct(a, tier)
			}
		case "group":
			if publicModel != "" {
				allowed, serr := s.groupAllowsModel(ctx, r.TargetID, publicModel)
				if serr != nil || !allowed {
					continue
				}
			}
			list, err := s.groupAccounts(ctx, r.TargetID)
			if err != nil {
				continue
			}
			for _, a := range list {
				a.GroupID = r.TargetID
				if publicModel != "" {
					upstreamModel, ok, serr := s.accountSupportsModel(ctx, a.ID, publicModel)
					if serr != nil || !ok {
						continue
					}
					a.UpstreamModel = upstreamModel
				}
				addAcct(a, tier)
			}
		}
	}
	if len(cands) == 0 {
		return nil, noop, ErrNoHealthyAccount
	}

	// Sticky reuse (§19): the bound account wins when still healthy AND not
	// full; context-rebuilding rules gate switching upstream of this call.
	if stickyID != "" {
		for _, c := range cands {
			if c.acct.ID == stickyID {
				if s.slots.Inflight(stickyID) < s.accountLimit(c.acct) && s.slots.AcquireWithSubscription(keyID, keyLimit, subscriptionID, subscriptionLimit, stickyID, s.accountLimit(c.acct)) {
					return c.acct, s.releaseFunc(keyID, subscriptionID, c.acct.ID), nil
				}
				break // sticky account unavailable/full: normal selection applies
			}
		}
	}

	// Walk route priority tiers in order; within a tier prefer least
	// weighted load, then priority, then rotation among equals (§19).
	// A weighted pool compares inflight/weight; ordinary pools and direct
	// accounts have weight 1, so the established least-loaded behavior is
	// preserved.
	tierStart := 0
	for tierStart < len(cands) {
		tierEnd := tierStart
		for tierEnd < len(cands) && cands[tierEnd].tier == cands[tierStart].tier {
			tierEnd++
		}
		tier := append([]cand(nil), cands[tierStart:tierEnd]...)
		loadScore := func(c cand) float64 {
			return float64(s.slots.Inflight(c.acct.ID)) / float64(c.weight)
		}
		sort.SliceStable(tier, func(i, j int) bool {
			ia, ib := loadScore(tier[i]), loadScore(tier[j])
			if ia != ib {
				return ia < ib
			}
			if tier[i].acct.Priority != tier[j].acct.Priority {
				return tier[i].acct.Priority < tier[j].acct.Priority
			}
			return tier[i].order < tier[j].order
		})
		best := -1.0
		bestPriority := 0
		var ties []cand
		for _, c := range tier {
			infl := s.slots.Inflight(c.acct.ID)
			if s.accountLimit(c.acct) > 0 && infl >= s.accountLimit(c.acct) {
				continue // full: never selected
			}
			score := loadScore(c)
			if best < 0 || (score == best && c.acct.Priority == bestPriority) {
				best = score
				bestPriority = c.acct.Priority
				ties = append(ties, c)
			} else {
				break
			}
		}
		start := 0
		if len(ties) > 0 {
			start = s.rotate(keyID) % len(ties)
		}
		for i := range ties {
			c := ties[(i+start)%len(ties)]
			if s.slots.AcquireWithSubscription(keyID, keyLimit, subscriptionID, subscriptionLimit, c.acct.ID, s.accountLimit(c.acct)) {
				return c.acct, s.releaseFunc(keyID, subscriptionID, c.acct.ID), nil
			}
			// lost the race for this slot: try the next tie
		}
		tierStart = tierEnd
	}
	return nil, noop, ErrConcurrency
}

func (s *Scheduler) groupAllowsModel(ctx context.Context, groupID, publicModel string) (bool, error) {
	var total, active int
	if err := s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status='active') FROM account_group_model_rules WHERE group_id=$1 AND public_model=$2`, groupID, publicModel).Scan(&total, &active); err != nil {
		return false, err
	}
	return total == 0 || active > 0, nil
}

func (s *Scheduler) accountSupportsModel(ctx context.Context, accountID, publicModel string) (string, bool, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM account_model_capabilities WHERE account_id=$1`, accountID).Scan(&total); err != nil {
		return "", false, err
	}
	if total == 0 {
		return publicModel, true, nil // legacy account: preserve prior routing behaviour
	}
	var upstream string
	err := s.pool.QueryRow(ctx, `SELECT upstream_model FROM account_model_capabilities WHERE account_id=$1 AND public_model=$2 AND status='active'`, accountID, publicModel).Scan(&upstream)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return upstream, true, nil
}

func (s *Scheduler) releaseFunc(keyID, subscriptionID, accountID string) func() {
	return func() { s.slots.Release(keyID, subscriptionID, accountID) }
}

func (s *Scheduler) rotate(keyID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rot[keyID]++
	return s.rot[keyID]
}

func (s *Scheduler) loadAccount(ctx context.Context, id, groupID string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx, `
		SELECT id, provider, label, COALESCE(upstream_base_url,''), state, concurrency_limit, priority, egress_policy_id, credential_version
		FROM accounts WHERE id=$1
		AND NOT EXISTS (SELECT 1 FROM account_holds h WHERE h.account_id=accounts.id)`, id).
		Scan(&a.ID, &a.Provider, &a.Label, &a.UpstreamBaseURL, &a.State, &a.ConcurrencyLimit, &a.Priority, &a.EgressPolicyID, &a.CredentialVersion)
	if err != nil {
		return nil, fmt.Errorf("account %s: %w", id, err)
	}
	a.GroupID = groupID
	return &a, nil
}

func (s *Scheduler) groupAccounts(ctx context.Context, groupID string) ([]*Account, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.provider, a.label, COALESCE(a.upstream_base_url,''), a.state, a.concurrency_limit, a.priority, a.egress_policy_id, a.credential_version,
		       g.strategy, m.weight, m.priority
		FROM account_group_members m JOIN accounts a ON a.id = m.account_id
		JOIN account_groups g ON g.id = m.group_id
		WHERE m.group_id=$1 AND a.state='active' AND g.status='active'
		AND NOT EXISTS (SELECT 1 FROM account_holds h WHERE h.account_id=a.id)
		ORDER BY a.priority, a.id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		var a Account
		var strategy string
		var memberWeight, memberPriority int
		if err := rows.Scan(&a.ID, &a.Provider, &a.Label, &a.UpstreamBaseURL, &a.State, &a.ConcurrencyLimit, &a.Priority, &a.EgressPolicyID, &a.CredentialVersion, &strategy, &memberWeight, &memberPriority); err != nil {
			return nil, err
		}
		// Pool-local settings deliberately override global account priority: an
		// account can participate in several pools with different strategies.
		switch strategy {
		case "weighted_round_robin":
			a.Priority = 100
			a.RouteWeight = memberWeight
		case "priority_failover":
			a.Priority = memberPriority
		default: // round_robin
			a.Priority = 100
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}
