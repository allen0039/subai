package billing

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestParsePriceCatalogConvertsPerTokenToPerMTok(t *testing.T) {
	body := []byte(`{
		"gpt-new": {
			"input_cost_per_token": 0.000002,
			"cache_read_input_token_cost": 0.0000002,
			"output_cost_per_token": 0.000010
		},
		"image-only": {"output_cost_per_image": 0.04},
		"sample_spec": {}
	}`)
	prices, err := parsePriceCatalog(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 1 {
		t.Fatalf("prices = %d, want 1", len(prices))
	}
	p := prices[0]
	if p.model != "gpt-new" || !p.input.Equal(decimal.NewFromInt(2)) ||
		!p.cached.Equal(decimal.RequireFromString("0.2")) || !p.output.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("unexpected price: %#v", p)
	}
}

func TestParsePriceCatalogFallsBackCachedPriceToInput(t *testing.T) {
	prices, err := parsePriceCatalog([]byte(`{"model":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 1 || !prices[0].cached.Equal(prices[0].input) {
		t.Fatalf("cached price did not fall back to input: %#v", prices)
	}
}
