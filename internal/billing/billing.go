// Package billing implements the dollar budget ledger: NUMERIC(30,12) storage,
// decimal math (no float accumulation, §18.1), timezone-aware periods, atomic
// reservation and idempotent settlement (§18.3).
package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

var (
	ErrBudgetExceeded = errors.New("budget exceeded")
	ErrNoPrice        = errors.New("no active price for model")
	ErrUnsupportedFee = errors.New("unsupported fixed fee dimension")
)

// ModelPrice is the frozen per-request price (§18.1).
type ModelPrice struct {
	Model              string
	InputPerMTok       decimal.Decimal
	CachedInputPerMTok decimal.Decimal
	OutputPerMTok      decimal.Decimal
	FixedFees          decimal.Decimal
}

type Usage struct {
	InputTokens  int64 // uncached
	CachedTokens int64
	OutputTokens int64
}

// Validate checks disjoint uncached, cached and output token counts.
func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.CachedTokens < 0 || u.OutputTokens < 0 {
		return errors.New("token counts must be non-negative")
	}
	return nil
}

// UsageFromTotal normalizes API total input into disjoint billing counts.
func UsageFromTotal(input, cached, output int64) (Usage, error) {
	if input < 0 || cached < 0 || output < 0 || cached > input {
		return Usage{}, errors.New("invalid usage: require non-negative counts and cached <= total input")
	}
	return Usage{InputTokens: input - cached, CachedTokens: cached, OutputTokens: output}, nil
}

// Cost implements §18.1: cost = (in*in_rate + cached*cached_rate + out*out_rate)/1e6 + fixed.
func (p *ModelPrice) Cost(u Usage) decimal.Decimal {
	mtok := decimal.NewFromInt(1_000_000)
	cost := p.InputPerMTok.Mul(dec(u.InputTokens)).
		Add(p.CachedInputPerMTok.Mul(dec(u.CachedTokens))).
		Add(p.OutputPerMTok.Mul(dec(u.OutputTokens))).
		Div(mtok).
		Add(p.FixedFees)
	if cost.IsNegative() {
		return decimal.Zero
	}
	return cost
}

func dec(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

// EstimateTokens is the conservative byte-based input bound (D-002):
// ceil(bytes/3) tokens with 1.5x safety factor.
func EstimateTokens(b []byte) int64 {
	base := (int64(len(b)) + 2) / 3
	return (base*3 + 1) / 2
}

// RoundUpReservation rounds up to the 1e-12 USD unit so the reservation never
// under-covers the computed bound (§18.1).
func RoundUpReservation(d decimal.Decimal) decimal.Decimal {
	up := d.Ceil() // coarse guard
	if d.Equal(up) {
		up = d
	}
	// exact: round to 12 places, then add one ulp of 1e-12 when truncation occurred
	r := d.RoundBank(12)
	if r.LessThan(d) {
		r = r.Add(decimal.New(1, -12))
	}
	return r
}

// ── Price versions ───────────────────────────────────────────────────────────

// RowQuerier is satisfied by both *pgxpool.Pool and pgx.Tx.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ActivePriceVersionID returns the active price version; without one, strict
// mode refuses to call (§8).
func ActivePriceVersion(ctx context.Context, q RowQuerier) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM price_versions WHERE status='active' ORDER BY activated_at DESC NULLS LAST LIMIT 1`).Scan(&id)
	if err != nil {
		return "", ErrNoPrice
	}
	return id, nil
}

func ModelPriceFor(ctx context.Context, q RowQuerier, versionID, model string) (*ModelPrice, error) {
	var p ModelPrice
	var fixed map[string]any
	err := q.QueryRow(ctx, `
		SELECT model, input_per_mtok, cached_input_per_mtok, output_per_mtok, fixed_fees
		FROM model_prices WHERE price_version_id=$1 AND model=$2`, versionID, model).
		Scan(&p.Model, &p.InputPerMTok, &p.CachedInputPerMTok, &p.OutputPerMTok, &fixed)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoPrice, model)
	}
	fixedTotal, err := fixedFeeTotal(fixed)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNoPrice, model, err)
	}
	p.FixedFees = fixedTotal
	return &p, nil
}

// fixedFeeTotal sums explicitly mapped fixed fees; unknown fee keys mean the
// caller cannot prove the upper bound and must reject in strict mode (§18.1).
func fixedFeeTotal(fixed map[string]any) (decimal.Decimal, error) {
	total := decimal.Zero
	for k, v := range fixed {
		switch k {
		case "supported":
			m, ok := v.(map[string]any)
			if !ok {
				return decimal.Zero, fmt.Errorf("supported fixed fees must be an object")
			}
			for name, amount := range m {
				s, ok := amount.(string)
				if !ok {
					return decimal.Zero, fmt.Errorf("supported fixed fee %q must be a decimal string", name)
				}
				d, err := decimal.NewFromString(s)
				if err != nil || d.IsNegative() {
					return decimal.Zero, fmt.Errorf("supported fixed fee %q is invalid", name)
				}
				total = total.Add(d)
			}
		default:
			// unmapped fee dimension: presence invalidates the upper bound
			return decimal.Zero, fmt.Errorf("%w: %s", ErrUnsupportedFee, k)
		}
	}
	return total, nil
}

// ── Budget periods (§18.2) ───────────────────────────────────────────────────

type PolicyScope struct {
	PolicyID    string
	OwnerType   string
	PeriodType  string // day|week|month
	Timezone    string
	Limit       decimal.Decimal
	periodStart time.Time
	periodEnd   time.Time
}

// applicablePolicies resolves every active budget policy constraining a
// request with scope (member, key, account, routed group).
func applicablePolicies(ctx context.Context, tx pgx.Tx, memberID, keyID, accountID, groupID string) ([]PolicyScope, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.owner_type, p.period, p.timezone, p.mode, p.amount, p.percent_bps, p.base_policy_id
		FROM budget_policies p
		WHERE p.status='active' AND (
			(p.owner_type='member' AND p.owner_member_id=$1)
		 OR (p.owner_type='key' AND p.owner_key_id=$2)
		 OR (p.owner_type='account' AND p.owner_account_id=$3)
		 OR (p.owner_type='group' AND p.owner_group_id=$4)
		 OR (p.owner_type='key_account' AND p.owner_key_id=$2 AND p.owner_account_id=$3)
		 OR (p.owner_type='key_group' AND p.owner_key_id=$2 AND p.owner_group_id=$4)
		 OR (p.owner_type='subscription' AND p.owner_subscription_id=(SELECT user_subscription_id FROM api_keys WHERE id=$2))
		)`, memberID, keyID, nullIfEmpty(accountID), nullIfEmpty(groupID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// R4-04: Read all policies first to avoid conn busy when querying base policies
	type policyRow struct {
		PolicyID   string
		OwnerType  string
		PeriodType string
		Timezone   string
		Mode       string
		Amount     decimal.NullDecimal
		Bps        *int
		BasePolicy *string
	}
	var policies []policyRow
	for rows.Next() {
		var p policyRow
		if err := rows.Scan(&p.PolicyID, &p.OwnerType, &p.PeriodType, &p.Timezone, &p.Mode, &p.Amount, &p.Bps, &p.BasePolicy); err != nil {
			return nil, err
		}
		policies = append(policies, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Now process policies and resolve base limits
	var scopes []PolicyScope
	for _, p := range policies {
		s := PolicyScope{
			PolicyID:   p.PolicyID,
			OwnerType:  p.OwnerType,
			PeriodType: p.PeriodType,
			Timezone:   p.Timezone,
		}
		switch p.Mode {
		case "fixed":
			s.Limit = p.Amount.Decimal
		case "percent":
			if p.BasePolicy == nil {
				return nil, fmt.Errorf("percent policy %s missing base_policy_id", s.PolicyID)
			}
			lim, err := basePolicyLimit(ctx, tx, *p.BasePolicy, s.PeriodType)
			if err != nil {
				return nil, err
			}
			s.Limit = lim.Mul(dec(int64(*p.Bps))).Div(decimal.NewFromInt(10000))
		}
		scopes = append(scopes, s)
	}
	return scopes, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func basePolicyLimit(ctx context.Context, tx pgx.Tx, baseID, period string) (decimal.Decimal, error) {
	var mode, basePeriod string
	var amount decimal.NullDecimal
	var baseBase *string
	err := tx.QueryRow(ctx, `SELECT mode, amount, period, base_policy_id FROM budget_policies WHERE id=$1 AND status='active'`, baseID).
		Scan(&mode, &amount, &basePeriod, &baseBase)
	if err != nil {
		return decimal.Zero, fmt.Errorf("percent base policy %s: %w", baseID, err)
	}
	if basePeriod != period {
		return decimal.Zero, fmt.Errorf("percent base policy %s has different period", baseID)
	}
	if mode != "fixed" || baseBase != nil {
		return decimal.Zero, fmt.Errorf("percent base policy %s must be fixed-mode (no percent-on-percent)", baseID)
	}
	return amount.Decimal, nil
}

// ensurePeriod returns the unique immutable period row for the scope at now,
// creating it with a limit snapshot on first touch. limit_snapshot is never
// overwritten on conflict (§18.2); only the end date is refreshed.
func ensurePeriod(ctx context.Context, tx pgx.Tx, s *PolicyScope, now time.Time) (periodID string, err error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return "", fmt.Errorf("policy %s timezone %q: %w", s.PolicyID, s.Timezone, err)
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	var end time.Time
	if s.PeriodType == "week" {
		weekday := int(start.Weekday()) // Sunday=0
		if weekday == 0 {
			weekday = 7
		}
		start = start.AddDate(0, 0, -(weekday - 1)) // Monday 00:00 in policy tz
		end = start.AddDate(0, 0, 7)
	} else if s.PeriodType == "month" {
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	} else {
		end = start.AddDate(0, 0, 1)
	}
	s.periodStart, s.periodEnd = start, end
	err = tx.QueryRow(ctx, `
		INSERT INTO budget_periods(policy_id, period_start, period_end, timezone, limit_snapshot)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (policy_id, period_start) DO UPDATE SET period_end=EXCLUDED.period_end
		RETURNING id, limit_snapshot`,
		s.PolicyID, start.Format("2006-01-02"), end.Format("2006-01-02"), s.Timezone, s.Limit).
		Scan(&periodID, &s.Limit)
	return periodID, err
}
