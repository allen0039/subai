package audit

import (
	"context"
	"testing"
)

func TestFilterExceptionsRequiresExactRuleAndNeverSkipsSecret(t *testing.T) {
	p := &Pipeline{ExceptionLookup: func(_ context.Context, key string, hash []byte, rule string) (bool, error) {
		return key == "key-1" && string(hash) == "content" && rule == "CONTENT-001", nil
	}}
	hits, err := p.filterExceptions(context.Background(), "key-1", []byte("content"), []RuleHit{
		{RuleID: "CONTENT-001", Category: "content", Action: "review"},
		{RuleID: "SECRET-001", Category: "secret", Action: "review"},
		{RuleID: "CONTENT-002", Category: "content", Action: "review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].RuleID != "SECRET-001" || hits[1].RuleID != "CONTENT-002" {
		t.Fatalf("unexpected retained hits: %#v", hits)
	}
}
