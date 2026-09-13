package gateway

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"subai/internal/accounts"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Request state machine (§19):
//
//	received → validated → local_checked → audit_queued → auditing
//	         → audit_passed → reserved → dispatching → streaming
//	         → settling → completed
//
// Terminal branches: rejected, audit_failed, cancelled_before_dispatch,
// failed_before_dispatch; after dispatching only unknown/settling.
// Every transition persists a timestamped history entry; the audit decision is
// durable before dispatching is allowed (§19).

type StateTracker struct {
	pool *pgxpool.Pool
}

func NewStateTracker(pool *pgxpool.Pool) *StateTracker { return &StateTracker{pool: pool} }

type historyEntry struct {
	State  string    `json:"state"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// Create inserts the request row in state received.
func (s *StateTracker) Create(ctx context.Context, reqID, keyID, clientReqID string, payloadHMAC []byte, model string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO requests(id, api_key_id, client_request_id, payload_hmac, model, state, state_history)
		VALUES($1,$2,$3,$4,$5,'received',$6::jsonb)`,
		reqID, keyID, clientReqID, payloadHMAC, model, mustJSON([]historyEntry{{State: "received", Reason: "accepted", At: time.Now().UTC()}}))
	return err
}

// SetSubscription records the dynamic subscription selected by the Key. It is
// kept separate from Create to retain compatibility with legacy Keys that have
// no subscription during the additive migration window.
func (s *StateTracker) SetSubscription(ctx context.Context, reqID, subscriptionID string) error {
	if subscriptionID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE requests SET user_subscription_id=$2::uuid, updated_at=now() WHERE id=$1`, reqID, subscriptionID)
	return err
}

// Transition moves the request to `state` with a reason. Transitions to a
// non-terminal state return the version check failure as error.
func (s *StateTracker) Transition(ctx context.Context, reqID, state, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE requests SET
			state = $2,
			error_code = CASE WHEN $3 <> '' THEN $3 ELSE error_code END,
			state_history = state_history || $4::jsonb,
			updated_at = now()
		WHERE id = $1`,
		reqID, state, reason, mustJSON([]historyEntry{{State: state, Reason: reason, At: time.Now().UTC()}}))
	return err
}

// SetRouting records account/group/coverage/price version on the request.
func (s *StateTracker) SetRouting(ctx context.Context, reqID, accountID, groupID, coverage, priceVersionID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE requests SET
			account_id = NULLIF($2,'')::uuid,
			group_id   = NULLIF($3,'')::uuid,
			coverage   = $4,
			price_version_id = NULLIF($5,'')::uuid,
			updated_at = now()
		WHERE id=$1`, reqID, accountID, groupID, coverage, priceVersionID)
	return err
}

// SetTerminalOutcome persists the terminal outcome type before settlement
// (P1-02). Recovery reads this field to distinguish completed vs failed.
func (s *StateTracker) SetTerminalOutcome(ctx context.Context, reqID, outcome string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE requests SET terminal_outcome = $2, updated_at = now()
		WHERE id = $1`, reqID, outcome)
	return err
}

// BumpAttempt increments attempt_id (used when retriable pre-dispatch errors
// occur) and returns the new attempt number.
func (s *StateTracker) BumpAttempt(ctx context.Context, reqID string) (int64, error) {
	var attempt int64
	err := s.pool.QueryRow(ctx,
		`UPDATE requests SET attempt_id = attempt_id + 1, updated_at=now() WHERE id=$1 RETURNING attempt_id`,
		reqID).Scan(&attempt)
	return attempt, err
}

// RecoverySummary reports what startup reconciliation did.
type RecoverySummary struct {
	Cancelled int64 // never-dispatched → cancelled_before_dispatch (funds released)
	Completed int64 // ledger already settled → reconciled to completed
	Failed    int64 // failed after dispatch → funds released, terminal state
	Unknown   int64 // possibly dispatched, unconfirmable → unknown (funds + account held)
	Swept     int64 // partial/legacy states converged by the idempotent sweep
}

// OnStartupRecovery implements crash recovery (§19 + reviews P1-4/R2-04).
// Each request converges inside ONE transaction (request state + reservations
// + account together), so a crash mid-recovery leaves either the old state or
// the fully converged state — never a half state. The sweep is idempotent and
// re-run safe: it also finishes convergence for requests already marked
// unknown/cancelled by an interrupted earlier pass or by older versions.
func (s *StateTracker) OnStartupRecovery(ctx context.Context) (RecoverySummary, error) {
	var summary RecoverySummary

	// 1. Requests still mid-flight in the state machine.
	rows, err := s.pool.Query(ctx, `
		SELECT id, state, COALESCE(account_id::text,''), COALESCE(terminal_outcome,'') FROM requests
		WHERE state IN ('received','validated','local_checked','audit_queued','auditing','audit_passed','reserved',
		                'dispatching','streaming','settling')`)
	if err != nil {
		return summary, err
	}
	type inflight struct {
		id, state, account, outcome string
	}
	var list []inflight
	for rows.Next() {
		var f inflight
		if err := rows.Scan(&f.id, &f.state, &f.account, &f.outcome); err != nil {
			rows.Close()
			return summary, err
		}
		list = append(list, f)
	}
	rows.Close()
	for _, f := range list {
		n, err := s.recoverOne(ctx, f.id, f.state, f.account, f.outcome)
		if err != nil {
			return summary, err
		}
		switch n {
		case "cancelled":
			summary.Cancelled++
		case "completed":
			summary.Completed++
		case "failed":
			summary.Failed++
		case "unknown":
			summary.Unknown++
		}
	}

	// 2. Idempotent sweep: converge partial outcomes from an interrupted
	// recovery (or legacy rows from older versions).
	if err := s.sweepPartialRecovery(ctx, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

// recoverOne converges exactly one request transactionally. Returns the
// terminal branch applied.
func (s *StateTracker) recoverOne(ctx context.Context, id, state, account, outcome string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	switch {
	case state == "dispatching" || state == "streaming" || state == "settling":
		var settled bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM usage_ledger WHERE request_id=$1 AND entry_type='charge')`, id).Scan(&settled); err != nil {
			return "", err
		}
		if settled {
			// Ledger is the source of truth: the charge committed, only the
			// state write was lost (review P1-4 crash window). P1-02: use
			// the durable terminal_outcome to distinguish completed vs failed.
			finalState := "completed"
			finalReason := "startup recovery: ledger already settled"
			if outcome == "failed" {
				finalState = "failed_after_dispatch"
				finalReason = "startup recovery: ledger settled, outcome was failed"
			}
			if err := s.appendHistoryTx(ctx, tx, id, finalState, finalReason); err != nil {
				return "", err
			}
			if err := tx.Commit(ctx); err != nil {
				return "", err
			}
			return outcome, nil
		}
		if err := s.convergeUnknownTx(ctx, tx, id, account, "startup recovery: dispatch outcome unconfirmable"); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "unknown", nil
	default:
		// Pre-dispatch: release funds and terminate in the same transaction.
		if err := releaseReservationsTx(ctx, tx, id, "released"); err != nil {
			return "", err
		}
		if err := s.appendHistoryTx(ctx, tx, id, "cancelled_before_dispatch", "startup recovery: never dispatched"); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "cancelled", nil
	}
}

// sweepPartialRecovery finishes convergence for rows an interrupted recovery
// pass (or an older version) left inconsistent:
//   - unknown requests with held reservations → reservations unknown + account held;
//   - cancelled_before_dispatch requests with held reservations → released;
//   - unknown requests with an active account → recovery_hold.
func (s *StateTracker) sweepPartialRecovery(ctx context.Context, summary *RecoverySummary) error {
	rows, err := s.pool.Query(ctx, `
		SELECT q.id, q.state, COALESCE(q.account_id::text,'')
		FROM requests q
		WHERE (q.state='unknown' AND (
		        EXISTS(SELECT 1 FROM reservations r WHERE r.request_id=q.id AND r.state='held')
		     OR EXISTS(SELECT 1 FROM accounts a WHERE a.id=q.account_id AND a.state='active')))
		   OR (q.state='cancelled_before_dispatch' AND
		       EXISTS(SELECT 1 FROM reservations r WHERE r.request_id=q.id AND r.state='held'))`)
	if err != nil {
		return err
	}
	type row struct {
		id, state, account string
	}
	var rowsList []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.state, &r.account); err != nil {
			rows.Close()
			return err
		}
		rowsList = append(rowsList, r)
	}
	rows.Close()
	for _, r := range rowsList {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		switch r.state {
		case "unknown":
			if err := s.convergeUnknownTx(ctx, tx, r.id, r.account, "startup recovery sweep: finish unknown convergence"); err != nil {
				return err
			}
		case "cancelled_before_dispatch":
			if err := releaseReservationsTx(ctx, tx, r.id, "released"); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		summary.Swept++
	}
	return nil
}

// convergeUnknownTx is the transactional unknown branch: request state,
// reservation states and account hold move together.
func (s *StateTracker) convergeUnknownTx(ctx context.Context, tx pgx.Tx, id, account, reason string) error {
	if err := accounts.AddHoldTx(ctx, tx, account, accounts.HoldReasonUnknownPending, map[string]any{"request_id": id}); err != nil {
		return err
	}
	if err := s.appendHistoryTx(ctx, tx, id, "unknown", reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE reservations SET state='unknown' WHERE request_id=$1 AND state='held'`, id); err != nil {
		return err
	}
	if account != "" {
		if _, err := tx.Exec(ctx,
			`UPDATE accounts SET state='recovery_hold', updated_at=now() WHERE id=$1::uuid AND state='active'`, account); err != nil {
			return err
		}
	}
	return nil
}

// releaseReservationsTx frees a request's held reservations and decrements
// the reserved counters in the caller's transaction.
func releaseReservationsTx(ctx context.Context, tx pgx.Tx, requestID, targetState string) error {
	rows, err := tx.Query(ctx,
		`SELECT id, budget_period_id, amount FROM reservations WHERE request_id=$1 AND state='held' FOR UPDATE`, requestID)
	if err != nil {
		return err
	}
	type rel struct {
		id, period string
		amount     string
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
			`UPDATE reservations SET state=$2, settled_at=now() WHERE id=$1`, r.id, targetState); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE budget_periods SET reserved = GREATEST(reserved - $1::numeric, 0) WHERE id=$2`, r.amount, r.period); err != nil {
			return err
		}
	}
	return nil
}

// appendHistoryTx writes the request state + history entry inside tx.
func (s *StateTracker) appendHistoryTx(ctx context.Context, tx pgx.Tx, reqID, state, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE requests SET
			state = $2,
			error_code = CASE WHEN $3 <> '' THEN $3 ELSE error_code END,
			state_history = state_history || $4::jsonb,
			updated_at = now()
		WHERE id = $1`,
		reqID, state, reason, mustJSON([]historyEntry{{State: state, Reason: reason, At: time.Now().UTC()}}))
	return err
}

// transitionWithHistory appends a history entry and sets the state.
func (s *StateTracker) transitionWithHistory(ctx context.Context, reqID, state, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE requests SET
			state = $2,
			error_code = CASE WHEN $3 <> '' THEN $3 ELSE error_code END,
			state_history = state_history || $4::jsonb,
			updated_at = now()
		WHERE id = $1`,
		reqID, state, reason, mustJSON([]historyEntry{{State: state, Reason: reason, At: time.Now().UTC()}}))
	return err
}

// RetainUnknown keeps the conservative concurrency hold note on the account
// (§18.3): accounts with unresolved unknown requests stay in recovery_hold
// until an admin resolves them.
func (s *StateTracker) RetainUnknown(ctx context.Context, reqID, accountID string) error {
	if accountID == "" {
		return nil
	}
	return accounts.AddHold(ctx, s.pool, accountID, accounts.HoldReasonUnknownPending, map[string]any{"request_id": reqID})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func NewRequestID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(b[:])
}
