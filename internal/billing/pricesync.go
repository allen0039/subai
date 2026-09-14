package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

const maxCatalogBytes = 64 << 20

type PriceSyncResult struct {
	VersionID string `json:"version_id,omitempty"`
	Models    int    `json:"models"`
	Hash      string `json:"source_hash"`
	Activated bool   `json:"activated"`
	Unchanged bool   `json:"unchanged"`
}

type catalogEntry struct {
	Input  *json.Number `json:"input_cost_per_token"`
	Cached *json.Number `json:"cache_read_input_token_cost"`
	Output *json.Number `json:"output_cost_per_token"`
}

type catalogPrice struct {
	model  string
	input  decimal.Decimal
	cached decimal.Decimal
	output decimal.Decimal
}

// SyncPriceCatalog imports a LiteLLM/Sub2API-compatible catalog into a new,
// immutable database price version and atomically activates it.
func SyncPriceCatalog(ctx context.Context, pool *pgxpool.Pool, client *http.Client, sourceURL, hashURL string) (PriceSyncResult, error) {
	var result PriceSyncResult
	if pool == nil {
		return result, errors.New("price sync database is unavailable")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	body, err := fetchPriceSource(ctx, client, sourceURL, maxCatalogBytes)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(body)
	result.Hash = hex.EncodeToString(digest[:])
	if strings.TrimSpace(hashURL) != "" {
		hashBody, err := fetchPriceSource(ctx, client, hashURL, 4096)
		if err != nil {
			return result, fmt.Errorf("fetch price hash: %w", err)
		}
		fields := strings.Fields(string(hashBody))
		if len(fields) == 0 {
			return result, errors.New("price hash source is empty")
		}
		expected := strings.ToLower(fields[0])
		if len(expected) != 64 || expected != result.Hash {
			return result, fmt.Errorf("price catalog SHA-256 mismatch")
		}
	}

	prices, err := parsePriceCatalog(body)
	if err != nil {
		return result, err
	}
	result.Models = len(prices)

	var existingID string
	err = pool.QueryRow(ctx, `SELECT id::text FROM price_versions WHERE status='active' AND source_hash=$1 LIMIT 1`, result.Hash).Scan(&existingID)
	if err == nil {
		result.VersionID = existingID
		result.Unchanged = true
		return result, nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `
		INSERT INTO price_versions(source_url,source_hash,fetched_at,origin,status,notes)
		VALUES($1,$2,now(),'catalog','draft',$3) RETURNING id::text`,
		sourceURL, result.Hash, fmt.Sprintf("Automatically imported %d models from the configured catalog", len(prices))).Scan(&result.VersionID); err != nil {
		return result, err
	}
	for _, price := range prices {
		if _, err := tx.Exec(ctx, `
			INSERT INTO model_prices(price_version_id,model,input_per_mtok,cached_input_per_mtok,output_per_mtok)
			VALUES($1,$2,$3,$4,$5)`, result.VersionID, price.model, price.input, price.cached, price.output); err != nil {
			return result, fmt.Errorf("insert catalog price for %s: %w", price.model, err)
		}
	}

	// Treat the active manual version as an override layer. This preserves
	// operator corrections while still adding newly discovered catalog models.
	if _, err := tx.Exec(ctx, `
		INSERT INTO model_prices(price_version_id,model,input_per_mtok,cached_input_per_mtok,output_per_mtok,fixed_fees,tiers,is_manual_override)
		SELECT $1,m.model,m.input_per_mtok,m.cached_input_per_mtok,m.output_per_mtok,m.fixed_fees,m.tiers,true
		FROM model_prices m JOIN price_versions v ON v.id=m.price_version_id
		WHERE v.status='active' AND v.origin='manual' AND m.is_manual_override
		ON CONFLICT(price_version_id,model) DO UPDATE SET
			input_per_mtok=EXCLUDED.input_per_mtok,
			cached_input_per_mtok=EXCLUDED.cached_input_per_mtok,
			output_per_mtok=EXCLUDED.output_per_mtok,
			fixed_fees=EXCLUDED.fixed_fees,
			tiers=EXCLUDED.tiers,
			is_manual_override=true`, result.VersionID); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `UPDATE price_versions SET status='superseded' WHERE status='active'`); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `UPDATE price_versions SET status='active',activated_at=now() WHERE id=$1`, result.VersionID); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	result.Activated = true
	return result, nil
}

func fetchPriceSource(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, errors.New("price source must use http or https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return nil, fmt.Errorf("price source returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("price source is too large")
	}
	return body, nil
}

func parsePriceCatalog(body []byte) ([]catalogPrice, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var raw map[string]catalogEntry
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse price catalog: %w", err)
	}
	prices := make([]catalogPrice, 0, len(raw))
	mtok := decimal.NewFromInt(1_000_000)
	for model, entry := range raw {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || model == "sample_spec" || entry.Input == nil || entry.Output == nil {
			continue
		}
		input, err := decimal.NewFromString(entry.Input.String())
		if err != nil || input.IsNegative() {
			return nil, fmt.Errorf("invalid input price for %s", model)
		}
		output, err := decimal.NewFromString(entry.Output.String())
		if err != nil || output.IsNegative() {
			return nil, fmt.Errorf("invalid output price for %s", model)
		}
		cached := input
		if entry.Cached != nil {
			cached, err = decimal.NewFromString(entry.Cached.String())
			if err != nil || cached.IsNegative() {
				return nil, fmt.Errorf("invalid cached input price for %s", model)
			}
		}
		prices = append(prices, catalogPrice{model: model, input: input.Mul(mtok), cached: cached.Mul(mtok), output: output.Mul(mtok)})
	}
	if len(prices) == 0 {
		return nil, errors.New("price catalog contains no token-priced models")
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i].model < prices[j].model })
	return prices, nil
}
