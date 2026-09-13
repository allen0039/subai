package integration

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"subai/internal/audit"
	"subai/internal/billing"
	"subai/internal/gateway"
)

func TestR4_01_MigrationSchemaCompatibility(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	var id string
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM accounts LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO account_holds(account_id,reason) VALUES($1,'over_reserve')`, id); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Migrate(ctx, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM account_holds WHERE account_id=$1 AND reason='over_reserve'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("hold lost: %d", n)
	}
}

func TestR4_04_PercentBudgetQueryNoBusy(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	var member, key, account, base string
	if err := e.db.Pool.QueryRow(ctx, `SELECT member_id,id FROM api_keys LIMIT 1`).Scan(&member, &key); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Pool.QueryRow(ctx, `SELECT owner_account_id,id FROM budget_policies WHERE owner_type='account'`).Scan(&account, &base); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE budget_policies SET mode='percent',amount=NULL,percent_bps=5000,base_policy_id=$1 WHERE owner_type='key'`, base); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO requests(id,api_key_id,account_id,model,state) VALUES('req_percent',$1,$2,'gpt-5-codex','reserved')`, key, account); err != nil {
		t.Fatal(err)
	}
	reservations, err := billing.Reserve(ctx, e.db.Pool, member, key, account, "", "req_percent", decimal.NewFromInt(10), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 2 {
		t.Fatalf("scopes=%d", len(reservations))
	}
	for _, r := range reservations {
		if !r.Amount.Equal(decimal.NewFromInt(10)) {
			t.Fatalf("amount=%s", r.Amount)
		}
	}
}

// R4-05: 审核规则动作处理完整性
func TestR4_05_AuditRuleActionHandling(t *testing.T) {
	tests := []struct {
		action          string
		wantDecision    audit.Decision
		shouldTerminate bool
	}{
		{"block", audit.DecisionBlock, true},
		{"review", audit.DecisionReview, true},
		{"reject", audit.DecisionBlock, true},
		{"unsupported", audit.DecisionUnsupported, true},
		{"flag", audit.DecisionAllow, false}, // flag doesn't terminate, continues to moderation
	}

	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			// Create a minimal rule and compile it
			rule := audit.Rule{
				ID:       "test-" + tt.action,
				Version:  1,
				Category: "content",
				Title:    "Test rule",
				Enabled:  true,
				Action:   tt.action,
				Severity: "high",
				Message:  "Test message",
				Matcher: audit.Matcher{
					Type:    "regex",
					Pattern: "test-pattern",
				},
			}

			rs, err := audit.Compile([]audit.Rule{rule}, 1)
			if err != nil {
				t.Fatalf("failed to compile ruleset: %v", err)
			}

			eng := audit.NewEngine(rs)

			// Test the engine's rule execution directly to verify action handling
			doc := &audit.AuditDocument{
				Segments: []audit.Segment{{Text: "test-pattern", Role: "user", Source: "input", Type: "text"}},
				Coverage: audit.CoverageFull,
			}

			hits, secretBlocked := eng.RunLocal(doc)

			// Verify hits are recorded for all actions
			if len(hits) == 0 {
				t.Fatalf("action %s: no hits recorded", tt.action)
			}

			if hits[0].Action != tt.action {
				t.Errorf("action %s: recorded action = %s, want %s", tt.action, hits[0].Action, tt.action)
			}

			// Verify secret blocking behavior
			if tt.action == "block" || tt.action == "review" {
				// For content category (not secret), secretBlocked should be false
				if secretBlocked {
					t.Errorf("action %s: content rule should not set secretBlocked", tt.action)
				}
			}

			// Now test that pipeline respects the action (only for terminating actions)
			if tt.shouldTerminate {
				cfg := audit.PipelineConfig{
					KeyScope:        "test-key",
					HMACKey:         []byte("test-hmac-key"),
					WaitQueueMax:    10,
					PerKeyQueue:     5,
					MaxInFlight:     2,
					WaitTimeout:     time.Second,
					TotalBudget:     5 * time.Second,
					CallTimeout:     2 * time.Second,
					Retries:         2,
					CacheTTL:        5 * time.Minute,
					ModerationModel: "omni-moderation-latest",
					Strict:          false,
				}
				pipe := audit.NewPipeline(eng, nil, cfg)

				res := pipe.Run(context.Background(), doc, "test-key-1")

				if res.Decision != tt.wantDecision {
					t.Errorf("action %s: decision = %s, want %s", tt.action, res.Decision, tt.wantDecision)
				}
				if len(res.Hits) == 0 {
					t.Errorf("action %s: no hits recorded in result", tt.action)
				}
			}
		})
	}
}

// R4-06: Usage只从终态事件提取
func TestR4_06_UsageOnlyFromTerminalEvents(t *testing.T) {
	tests := []struct {
		eventType string
		wantOK    bool
	}{
		{"response.completed", true},
		{"response.failed", true},
		{"response.content_block_start", false},
		{"response.content_block_delta", false},
		{"response.output_tokens", false},
		{"ping", false},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			frame := gateway.SSEFrame{
				Event: tt.eventType,
				Data: []byte(`{
					"response": {
						"usage": {
							"input_tokens": 100,
							"output_tokens": 50
						}
					}
				}`),
			}

			usage, ok := gateway.ExtractUsageFromEvent(frame)
			if ok != tt.wantOK {
				t.Errorf("event %s: ok = %v, want %v", tt.eventType, ok, tt.wantOK)
			}
			if tt.wantOK && (usage.InputTokens != 100 || usage.OutputTokens != 50) {
				t.Errorf("event %s: usage = %+v, want {100, 50}", tt.eventType, usage)
			}
		})
	}

	// Test incomplete usage in terminal event
	incompleteFrame := gateway.SSEFrame{
		Event: "response.completed",
		Data: []byte(`{
			"response": {
				"usage": {
					"input_tokens": 100
				}
			}
		}`),
	}
	_, ok := gateway.ExtractUsageFromEvent(incompleteFrame)
	if ok {
		t.Error("incomplete usage in terminal event should return ok=false")
	}
}

// R4-07: 负token验证
func TestR4_07_NegativeTokenValidation(t *testing.T) {
	tests := []struct {
		name         string
		inputTokens  int64
		cachedTokens int64
		outputTokens int64
		wantErr      bool
	}{
		{"valid", 100, 20, 50, false},
		{"negative_input", -10, 0, 50, true},
		{"negative_cached", 100, -5, 50, true},
		{"negative_output", 100, 20, -30, true},
		{"cached_exceeds_input", 100, 150, 50, true},
		{"zero_valid", 0, 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := billing.UsageFromTotal(tt.inputTokens, tt.cachedTokens, tt.outputTokens)
			hasErr := err != nil

			if hasErr != tt.wantErr {
				t.Errorf("validation error = %v, want %v", hasErr, tt.wantErr)
			}
		})
	}
}
