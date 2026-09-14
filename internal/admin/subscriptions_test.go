package admin

import "testing"

func TestNormalizePlanInputDefaultsToUnlimitedKeys(t *testing.T) {
	in := planInput{Name: "基础套餐"}
	if err := normalizePlanInput(&in); err != nil {
		t.Fatalf("normalize plan: %v", err)
	}
	if in.MaxKeys != 0 {
		t.Fatalf("max keys = %d, want 0 (unlimited)", in.MaxKeys)
	}
	if in.ConcurrencyLimit != 1 {
		t.Fatalf("concurrency = %d, want default 1", in.ConcurrencyLimit)
	}
}

func TestNormalizePlanInputRejectsNegativeKeyLimit(t *testing.T) {
	in := planInput{Name: "错误套餐", MaxKeys: -1}
	if err := normalizePlanInput(&in); err == nil {
		t.Fatal("negative max keys was accepted")
	}
}
