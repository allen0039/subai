package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"subai/internal/accounts"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// Reservation is one scope's held amount for a request.
type Reservation struct {
	ID             string
	BudgetPeriodID string
	Amount         decimal.Decimal
	State          string
}

// Reserve atomically holds `candidate` on every applicable budget scope
// (§18.3): rows are locked in deterministic (policy id) order, each scope must
// satisfy spent+reserved+candidate <= limit_snapshot, then the reservation rows
// and the reserved counters are written in the same transaction. Any failure
// rolls back every scope — partial holds are impossible.
func Reserve(ctx context.Context, pool Pool, memberID, keyID, accountID, groupID, requestID string, candidate decimal.Decimal, now time.Time) ([]Reservation, error) {
	return reserve(ctx, pool, memberID, keyID, accountID, groupID, requestID, candidate, now, "strict_reservation")
}

// AdmitMetered performs Sub2API-style preflight admission. It snapshots every
// applicable budget period but holds no estimated money; the actual charge is
// applied after the upstream reports usage.
func AdmitMetered(ctx context.Context, pool Pool, memberID, keyID, accountID, groupID, requestID string, now time.Time) ([]Reservation, error) {
	return reserve(ctx, pool, memberID, keyID, accountID, groupID, requestID, decimal.Zero, now, "metered")
}

func reserve(ctx context.Context, pool Pool, memberID, keyID, accountID, groupID, requestID string, candidate decimal.Decimal, now time.Time, mode string) ([]Reservation, error) {
	if candidate.IsNegative() {
		return nil, errors.New("negative reservation")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	scopes, err := applicablePolicies(ctx, tx, memberID, keyID, accountID, groupID)
	if err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		if mode == "metered" {
			// Subscription limits are optional in metered mode. With no policy,
			// usage is still recorded in the ledger but no period is constrained.
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			return []Reservation{}, nil
		}
		// Strict reservation mode requires a configured upper bound.
		return nil, fmt.Errorf("%w: no budget policy covers this request", ErrBudgetExceeded)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].PolicyID < scopes[j].PolicyID })

	var out []Reservation
	for i := range scopes {
		s := &scopes[i]
		periodID, err := ensurePeriod(ctx, tx, s, now)
		if err != nil {
			return nil, err
		}
		var spent, reserved, limit decimal.Decimal
		err = tx.QueryRow(ctx,
			`SELECT spent, reserved, limit_snapshot FROM budget_periods WHERE id=$1 FOR UPDATE`, periodID).
			Scan(&spent, &reserved, &limit)
		if err != nil {
			return nil, err
		}
		exceeded := spent.Add(reserved).Add(candidate).GreaterThan(limit)
		if mode == "metered" {
			exceeded = !spent.LessThan(limit)
		}
		if exceeded {
			return nil, fmt.Errorf("%w: policy %s (spent %s + reserved %s + candidate %s > limit %s)",
				ErrBudgetExceeded, s.PolicyID, spent, reserved, candidate, limit)
		}
		var resID string
		err = tx.QueryRow(ctx, `
			INSERT INTO reservations(request_id, budget_period_id, amount, state, billing_mode)
			VALUES($1,$2,$3,'held',$4) RETURNING id`, requestID, periodID, candidate, mode).Scan(&resID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE budget_periods SET reserved = reserved + $1 WHERE id=$2`, candidate, periodID); err != nil {
			return nil, err
		}
		out = append(out, Reservation{ID: resID, BudgetPeriodID: periodID, Amount: candidate, State: "held"})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// Release frees held reservations for a request that never reached upstream
// (cancelled_before_dispatch / failed_before_dispatch, §18.3).
func Release(ctx context.Context, pool Pool, requestID string) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, budget_period_id, amount FROM reservations WHERE request_id=$1 AND state='held' FOR UPDATE`, requestID)
		if err != nil {
			return err
		}
		type rel struct {
			id, period string
			amount     decimal.Decimal
		}
		var rels []rel
		for rows.Next() {
			var r rel
			if err := rows.Scan(&r.id, &r.period, &r.amount); err != nil {
				rows.Close()
				return err
			}
			rels = append(rels, r)
		}
		rows.Close()
		for _, r := range rels {
			if _, err := tx.Exec(ctx,
				`UPDATE reservations SET state='released', settled_at=now() WHERE id=$1`, r.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE budget_periods SET reserved = reserved - $1 WHERE id=$2`, r.amount, r.period); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkUnknown moves held reservations to unknown on unconfirmable dispatches:
// funds stay reserved and are never auto-refunded (§18.3, §19).
// Review R3-01: also adds unknown_pending hold when accountID is provided.
func MarkUnknown(ctx context.Context, pool Pool, requestID string) error {
	return MarkUnknownWithAccount(ctx, pool, requestID, "")
}

// MarkUnknownWithAccount marks unknown and optionally adds account hold.
func MarkUnknownWithAccount(ctx context.Context, pool Pool, requestID, accountID string) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		if accountID == "" {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(account_id::text,'') FROM requests WHERE id=$1`, requestID).Scan(&accountID); err != nil {
				return err
			}
		}
		if err := accounts.AddHoldTx(ctx, tx, accountID, accounts.HoldReasonUnknownPending, map[string]any{"request_id": requestID}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE reservations SET state='unknown' WHERE request_id=$1 AND state='held'`, requestID)
		return err
	})
}

// SettleResult reports what settlement did.
type SettleResult struct {
	Settled       bool // false when the (request, attempt) was already settled
	Cost          decimal.Decimal
	OverReserve   bool // actual cost exceeded a reservation — strict-budget violation signal (§18.3)
	ScopesApplied int
}

// Settle records the usage charge exactly once per (request_id, attempt_id)
// and reconciles every reservation scope. actual cost is never truncated to
// the reserved amount (§18.3); over-reservation flips the account into
// recovery_hold and raises an admin event.
func Settle(ctx context.Context, pool Pool, requestID string, attemptID int64, keyID, accountID string, usage Usage, catalogPrice, price *ModelPrice, priceVersionID string) (SettleResult, error) {
	var result SettleResult
	if err := usage.Validate(); err != nil {
		return result, err
	}
	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		if err := accounts.LockTx(ctx, tx, accountID); err != nil {
			return err
		}
		baseCost := catalogPrice.BaseCost(usage)
		pricingBaseCost := price.BaseCost(usage)
		cost := price.Cost(usage)
		result.Cost = cost
		// Idempotent insert: the unique (request_id, attempt_id, entry_type)
		// index is the source of truth; ON CONFLICT DO NOTHING makes racing
		// settlements converge on exactly one charge row (§18.3).
		tag, err := tx.Exec(ctx, `
			INSERT INTO usage_ledger(request_id, attempt_id, api_key_id, account_id, user_subscription_id,
				input_tokens, cached_input_tokens, output_tokens, base_cost, pricing_base_cost, rate_multiplier, cost, price_version_id, entry_type, details)
			VALUES($1,$2,$3,$4,(SELECT user_subscription_id FROM api_keys WHERE id=$3),$5,$6,$7,$8,$9,$10,$11,$12,'charge',$13)
			ON CONFLICT (request_id, attempt_id, entry_type) DO NOTHING`,
			requestID, attemptID, keyID, nullIfEmpty(accountID),
			usage.InputTokens, usage.CachedTokens, usage.OutputTokens, baseCost, pricingBaseCost, price.EffectiveRateMultiplier(), cost, nullIfEmpty(priceVersionID),
			jsonRaw(`{"settled_at_flow":"normal"}`))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			result.Settled = false
			return nil
		}

		rows, err := tx.Query(ctx, `
			SELECT r.id, r.budget_period_id, r.amount, r.state, r.billing_mode FROM reservations r
			WHERE r.request_id=$1 AND r.state IN ('held','unknown') FOR UPDATE`, requestID)
		if err != nil {
			return err
		}
		type rs struct {
			id, period, state, mode string
			amount                  decimal.Decimal
		}
		var rsList []rs
		for rows.Next() {
			var r rs
			if err := rows.Scan(&r.id, &r.period, &r.amount, &r.state, &r.mode); err != nil {
				rows.Close()
				return err
			}
			rsList = append(rsList, r)
		}
		rows.Close()

		for _, r := range rsList {
			if _, err := tx.Exec(ctx, `
				UPDATE budget_periods SET
					spent = spent + $1,
					reserved = GREATEST(reserved - $2, 0)
				WHERE id=$3`, cost, r.amount, r.period); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE reservations SET state='settled', settled_at=now() WHERE id=$1`, r.id); err != nil {
				return err
			}
			result.ScopesApplied++
			if r.mode != "metered" && cost.GreaterThan(r.amount) {
				result.OverReserve = true
			}
		}
		if result.OverReserve {
			// As-ledger accounting happened above; now pause capability + alert (§18.3).
			// Review R3-01: record hold reason in account_holds table.
			if err := accounts.AddHoldTx(ctx, tx, accountID, accounts.HoldReasonOverReserve, map[string]any{"request_id": requestID}); err != nil {
				return err
			}

			changes, _ := json.Marshal(map[string]any{
				"request_id": requestID, "actual": cost.String(), "over_reservation": true,
			})
			if _, err := tx.Exec(ctx, `
				INSERT INTO admin_events(actor, action, target_type, target_id, changes, request_id)
				VALUES('billing','budget.over_reserve','account',$1,$2,$3)`,
				accountID, changes, requestID); err != nil {
				return err
			}
		}
		result.Settled = true
		return nil
	})
	return result, err
}

// AdjustUnknown applies an evidence-driven manual settlement for a request in
// unknown state: appends an `adjustment` ledger row and reconciles the held
// funds. No timeout may do this automatically (§18.3).
func AdjustUnknown(ctx context.Context, pool Pool, requestID string, actor string, usage Usage, catalogPrice, price *ModelPrice, priceVersionID string) (AdjustResult, error) {
	var result AdjustResult
	if err := usage.Validate(); err != nil {
		return result, err
	}
	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		var account string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(account_id::text,'') FROM requests WHERE id=$1`, requestID).Scan(&account); err != nil {
			return err
		}
		if err := accounts.LockTx(ctx, tx, account); err != nil {
			return err
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&state); err != nil {
			return err
		}
		if state != "unknown" {
			return fmt.Errorf("request %s is %s, not unknown", requestID, state)
		}
		var keyID *string
		var accountID *string
		if err := tx.QueryRow(ctx, `SELECT api_key_id, account_id FROM requests WHERE id=$1`, requestID).Scan(&keyID, &accountID); err != nil {
			return err
		}
		result.Cost = price.Cost(usage)
		if _, err := tx.Exec(ctx, `
			INSERT INTO usage_ledger(request_id, attempt_id, api_key_id, account_id,
				input_tokens, cached_input_tokens, output_tokens, base_cost, pricing_base_cost, rate_multiplier, cost, price_version_id, entry_type, details)
			VALUES($1,0,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'adjustment',$12)
			ON CONFLICT (request_id, attempt_id, entry_type) DO NOTHING`,
			requestID, *keyID, accountID, usage.InputTokens, usage.CachedTokens, usage.OutputTokens,
			catalogPrice.BaseCost(usage), price.BaseCost(usage), price.EffectiveRateMultiplier(), result.Cost, nullIfEmpty(priceVersionID), jsonRaw(fmt.Sprintf(`{"actor":%q,"source":"manual_evidence"}`, actor))); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT id, budget_period_id, amount FROM reservations WHERE request_id=$1 AND state='unknown' FOR UPDATE`, requestID)
		if err != nil {
			return err
		}
		type r struct {
			id, period string
			amount     decimal.Decimal
		}
		var list []r
		for rows.Next() {
			var e r
			if err := rows.Scan(&e.id, &e.period, &e.amount); err != nil {
				rows.Close()
				return err
			}
			list = append(list, e)
		}
		rows.Close()
		for _, e := range list {
			if _, err := tx.Exec(ctx, `
				UPDATE budget_periods SET spent = spent + $1, reserved = GREATEST(reserved - $2, 0) WHERE id=$3`,
				result.Cost, e.amount, e.period); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE reservations SET state='settled', settled_at=now() WHERE id=$1`, e.id); err != nil {
				return err
			}
		}
		// Terminal state + over-reserve policy identical to normal settlement:
		// actual is never truncated (§18.3, review P2-9).
		result.OverReserve = false
		for _, e := range list {
			if result.Cost.GreaterThan(e.amount) {
				result.OverReserve = true
			}
		}
		finalState := "completed"
		errCode := "unknown_resolved"
		if result.OverReserve {
			errCode = "unknown_resolved_over_reserve"
			// Review R3-01: record hold reason in account_holds table.
			if err := accounts.AddHoldTx(ctx, tx, account, accounts.HoldReasonOverReserve, map[string]any{"request_id": requestID}); err != nil {
				return err
			}

			changes, _ := json.Marshal(map[string]any{
				"request_id": requestID, "actual": result.Cost.String(), "over_reservation": true, "source": "manual_adjustment",
			})
			if _, err := tx.Exec(ctx, `
				INSERT INTO admin_events(actor, action, target_type, target_id, changes, request_id)
				VALUES($1,'budget.over_reserve','account',COALESCE($2,''),$3,$4)`,
				actor, accountID, changes, requestID); err != nil {
				return err
			}
		}
		if _, exErr := tx.Exec(ctx,
			`UPDATE requests SET state=$2, error_code=$3, updated_at=now() WHERE id=$1`,
			requestID, finalState, errCode); exErr != nil {
			return exErr
		}
		recovered, err := accounts.ResolveUnknownTx(ctx, tx, account)
		result.AccountRecovered = recovered
		return err

	})
	return result, err
}

// AdjustResult reports the manual settlement outcome (review R2-02).
type AdjustResult struct {
	AccountRecovered bool
	Cost             decimal.Decimal
	OverReserve      bool // actual exceeded reservation: isolation persists
}

// AccountHasOpenUnknowns reports whether any reservation for the account's
// requests is still in unknown state (review P2-9: recover only when clear).
func AccountHasOpenUnknowns(ctx context.Context, pool *pgxpool.Pool, accountID string) (bool, error) {
	var n int64
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM reservations r
		JOIN requests q ON q.id = r.request_id
		WHERE q.account_id=$1::uuid AND r.state='unknown'`, accountID).Scan(&n)
	return n > 0, err
}

type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type jsonRaw string

func withTx(ctx context.Context, pool Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
