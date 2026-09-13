package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"subai/internal/storage"
)

func TestSubscriptionKeyUsesDynamicPlanPoolAndStopsWhenSuspended(t *testing.T) {
	requireDB(t)
	e := newTestEnv(t)
	ctx := context.Background()

	var memberID, poolID string
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO members(name,role) VALUES('subscriber','member') RETURNING id::text`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT id::text FROM account_groups LIMIT 1`).Scan(&poolID); err != nil {
		t.Fatal(err)
	}
	var planID, versionID string
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO plans(name,status) VALUES('starter','active') RETURNING id::text`).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO plan_versions(plan_id,version_number,concurrency_limit,max_keys,default_validity_days)
		VALUES($1,1,1,2,30) RETURNING id::text`, planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE plans SET current_version_id=$2 WHERE id=$1`, planID, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO plan_pool_bindings(plan_version_id,pool_id) VALUES($1,$2)`, versionID, poolID); err != nil {
		t.Fatal(err)
	}
	var subscriptionID string
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO user_subscriptions(member_id,plan_version_id,status,starts_at,expires_at)
		VALUES($1,$2,'active',now()-interval '1 minute',now()+interval '1 day') RETURNING id::text`, memberID, versionID).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO budget_policies(owner_type,owner_subscription_id,period,timezone,mode,amount)
		VALUES('subscription',$1,'day','UTC','fixed',100)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	key := "sk-subai-" + strings.Repeat("s", 40)
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO api_keys(member_id,user_subscription_id,name,public_prefix,key_hash,concurrency_limit)
		VALUES($1,$2,'subscription-key','sk-subai-', $3,1)`, memberID, subscriptionID, storage.HashToken(key)); err != nil {
		t.Fatal(err)
	}

	first := e.post(t, "/v1/responses", key, body(true, "subscription request"))
	if first.Code != http.StatusOK {
		t.Fatalf("subscription request status=%d body=%s", first.Code, first.Body.String())
	}
	var recorded string
	if err := e.db.Pool.QueryRow(ctx, `SELECT user_subscription_id::text FROM requests ORDER BY created_at DESC LIMIT 1`).Scan(&recorded); err != nil || recorded != subscriptionID {
		t.Fatalf("request subscription=%q err=%v want=%q", recorded, err, subscriptionID)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE user_subscriptions SET status='suspended' WHERE id=$1`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	second := e.post(t, "/v1/responses", key, body(true, "must not dispatch"))
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("suspended subscription status=%d body=%s", second.Code, second.Body.String())
	}
	if got := e.up.count(); got != 1 {
		t.Fatalf("upstream count=%d want 1; suspended subscription must block before dispatch", got)
	}
}
