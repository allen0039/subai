package billing

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestUsageFromTotal(t *testing.T) {
	tests := []struct {
		name    string
		input   int64
		cached  int64
		output  int64
		want    Usage
		wantErr bool
	}{
		{
			name:   "normal case with cached tokens",
			input:  100,
			cached: 80,
			output: 50,
			want:   Usage{InputTokens: 20, CachedTokens: 80, OutputTokens: 50},
		},
		{
			name:   "no cached tokens",
			input:  100,
			cached: 0,
			output: 50,
			want:   Usage{InputTokens: 100, CachedTokens: 0, OutputTokens: 50},
		},
		{
			name:   "all tokens cached",
			input:  100,
			cached: 100,
			output: 50,
			want:   Usage{InputTokens: 0, CachedTokens: 100, OutputTokens: 50},
		},
		{
			name:    "cached exceeds input",
			input:   100,
			cached:  150,
			output:  50,
			wantErr: true,
		},
		{
			name:    "negative input",
			input:   -10,
			cached:  0,
			output:  50,
			wantErr: true,
		},
		{
			name:    "negative cached",
			input:   100,
			cached:  -5,
			output:  50,
			wantErr: true,
		},
		{
			name:    "negative output",
			input:   100,
			cached:  80,
			output:  -30,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UsageFromTotal(tt.input, tt.cached, tt.output)
			if tt.wantErr {
				if err == nil {
					t.Errorf("UsageFromTotal() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("UsageFromTotal() unexpected error: %v", err)
				return
			}
			if got.InputTokens != tt.want.InputTokens {
				t.Errorf("InputTokens = %d, want %d", got.InputTokens, tt.want.InputTokens)
			}
			if got.CachedTokens != tt.want.CachedTokens {
				t.Errorf("CachedTokens = %d, want %d", got.CachedTokens, tt.want.CachedTokens)
			}
			if got.OutputTokens != tt.want.OutputTokens {
				t.Errorf("OutputTokens = %d, want %d", got.OutputTokens, tt.want.OutputTokens)
			}
		})
	}
}

func TestUsageValidate(t *testing.T) {
	tests := []struct {
		name    string
		usage   Usage
		wantErr bool
	}{
		{
			name:    "valid usage",
			usage:   Usage{InputTokens: 100, CachedTokens: 80, OutputTokens: 50},
			wantErr: false,
		},
		{
			name:    "negative input tokens",
			usage:   Usage{InputTokens: -10, CachedTokens: 80, OutputTokens: 50},
			wantErr: true,
		},
		{
			name:    "negative cached tokens",
			usage:   Usage{InputTokens: 100, CachedTokens: -5, OutputTokens: 50},
			wantErr: true,
		},
		{
			name:    "negative output tokens",
			usage:   Usage{InputTokens: 100, CachedTokens: 80, OutputTokens: -30},
			wantErr: true,
		},
		{
			name:    "zero tokens",
			usage:   Usage{InputTokens: 0, CachedTokens: 0, OutputTokens: 0},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.usage.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

func TestModelPriceCost(t *testing.T) {
	price := &ModelPrice{
		Model:              "test-model",
		InputPerMTok:       decimal.NewFromInt(2),    // $2/million
		CachedInputPerMTok: decimal.NewFromFloat(.2), // $0.2/million
		OutputPerMTok:      decimal.NewFromInt(10),   // $10/million
		FixedFees:          decimal.Zero,
	}

	tests := []struct {
		name     string
		usage    Usage
		wantCost string
	}{
		{
			name:     "R5-01 case: 20 uncached + 80 cached + 0 output",
			usage:    Usage{InputTokens: 20, CachedTokens: 80, OutputTokens: 0},
			wantCost: "0.000056", // (20*2 + 80*0.2)/1e6
		},
		{
			name:     "all uncached",
			usage:    Usage{InputTokens: 100, CachedTokens: 0, OutputTokens: 50},
			wantCost: "0.000700", // (100*2 + 50*10)/1e6
		},
		{
			name:     "all cached",
			usage:    Usage{InputTokens: 0, CachedTokens: 100, OutputTokens: 50},
			wantCost: "0.000520", // (100*0.2 + 50*10)/1e6
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := price.Cost(tt.usage)
			if got.StringFixed(6) != tt.wantCost {
				t.Errorf("Cost() = %s, want %s", got.StringFixed(6), tt.wantCost)
			}
		})
	}
}

func TestModelPriceAppliesPlanRateMultiplier(t *testing.T) {
	price := (&ModelPrice{
		Model:              "test-model",
		InputPerMTok:       decimal.NewFromInt(2),
		CachedInputPerMTok: decimal.NewFromFloat(.2),
		OutputPerMTok:      decimal.NewFromInt(10),
	}).WithRateMultiplier(decimal.RequireFromString("1.25"))
	usage := Usage{InputTokens: 100, CachedTokens: 50, OutputTokens: 20}
	if got := price.BaseCost(usage); !got.Equal(decimal.RequireFromString("0.00041")) {
		t.Fatalf("BaseCost() = %s", got)
	}
	if got := price.Cost(usage); !got.Equal(decimal.RequireFromString("0.0005125")) {
		t.Fatalf("Cost() = %s", got)
	}
}

func TestModelPriceAllowsZeroRateMultiplier(t *testing.T) {
	price := (&ModelPrice{
		InputPerMTok:  decimal.RequireFromString("2"),
		OutputPerMTok: decimal.RequireFromString("10"),
	}).WithRateMultiplier(decimal.Zero)

	if got := price.Cost(Usage{InputTokens: 1_000, OutputTokens: 100}); !got.IsZero() {
		t.Fatalf("free plan cost = %s, want 0", got)
	}
	if got := price.BaseCost(Usage{InputTokens: 1_000, OutputTokens: 100}); !got.Equal(decimal.RequireFromString("0.003")) {
		t.Fatalf("base cost = %s, want 0.003", got)
	}
}

func TestFixedFeeTotalRejectsUnknownDimensions(t *testing.T) {
	_, err := fixedFeeTotal(map[string]any{"unmapped_fee": "1"})
	if !errors.Is(err, ErrUnsupportedFee) {
		t.Fatalf("fixedFeeTotal() error = %v, want ErrUnsupportedFee", err)
	}
	got, err := fixedFeeTotal(map[string]any{"supported": map[string]any{"request": "0.001", "image": "0.02"}})
	if err != nil || !got.Equal(decimal.RequireFromString("0.021")) {
		t.Fatalf("fixedFeeTotal() = %s, %v", got, err)
	}
}
