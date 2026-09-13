package accounts

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HoldReason string

const (
	HoldReasonOverReserve    HoldReason = "over_reserve"
	HoldReasonUnknownPending HoldReason = "unknown_pending"
	HoldReasonAdminPause     HoldReason = "admin_pause"
	HoldReasonReauthRequired HoldReason = "reauth_required"
)

// LockTx must precede request and reservation locks in account-scoped mutations.
func LockTx(ctx context.Context, tx pgx.Tx, id string) error {
	if id == "" {
		return nil
	}
	var locked string
	return tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE id=$1 FOR UPDATE`, id).Scan(&locked)
}

func AddHoldTx(ctx context.Context, tx pgx.Tx, id string, reason HoldReason, details map[string]any) error {
	if id == "" {
		return nil
	}
	if err := LockTx(ctx, tx, id); err != nil {
		return err
	}
	if details == nil {
		details = map[string]any{}
	}
	data, err := json.Marshal(details)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_holds(account_id,reason,details) VALUES($1,$2,$3) ON CONFLICT(account_id,reason) DO UPDATE SET details=EXCLUDED.details`, id, string(reason), data); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE accounts SET state='recovery_hold',updated_at=now() WHERE id=$1 AND state IN ('active','refreshing')`, id)
	return err
}

// ResolveUnknownTx clears only the derived unknown reason, under the account lock.
func ResolveUnknownTx(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	if err := LockTx(ctx, tx, id); err != nil {
		return false, err
	}
	var open bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM requests q WHERE q.account_id=$1 AND (q.state='unknown' OR EXISTS(SELECT 1 FROM reservations r WHERE r.request_id=q.id AND r.state='unknown')))`, id).Scan(&open)
	if err != nil || open {
		return false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM account_holds WHERE account_id=$1 AND reason='unknown_pending'`, id); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE accounts SET state='active',updated_at=now() WHERE id=$1 AND state='recovery_hold' AND NOT EXISTS(SELECT 1 FROM account_holds WHERE account_id=$1)`, id)
	return tag.RowsAffected() > 0, err
}

func AddHold(ctx context.Context, pool *pgxpool.Pool, id string, reason HoldReason, details map[string]any) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error { return AddHoldTx(ctx, tx, id, reason, details) })
}
func RemoveHold(ctx context.Context, pool *pgxpool.Pool, id string, reason HoldReason) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		if err := LockTx(ctx, tx, id); err != nil {
			return err
		}
		if reason != HoldReasonUnknownPending {
			if _, err := tx.Exec(ctx, `DELETE FROM account_holds WHERE account_id=$1 AND reason=$2`, id, string(reason)); err != nil {
				return err
			}
		}
		_, err := ResolveUnknownTx(ctx, tx, id)
		return err
	})
}
func HasHold(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	var held bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_holds WHERE account_id=$1)`, id).Scan(&held)
	return held, err
}
func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
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
