package storage

import (
	"context"
	"encoding/json"
	"time"
)

// LogAdminEvent records an administrative action. Callers must sanitize
// before/after values: no secrets, tokens or proxy passwords ever land here
// (§16.2, review item 5).
func (d *DB) LogAdminEvent(ctx context.Context, actor, action, targetType, targetID string, changes map[string]any, requestID string) {
	if changes == nil {
		changes = map[string]any{}
	}
	b, _ := json.Marshal(changes)
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO admin_events(actor, action, target_type, target_id, changes, request_id)
		VALUES($1,$2,$3,$4,$5,$6)`,
		actor, action, targetType, targetID, b, requestID)
	if err != nil {
		// Admin events are best-effort for non-auditable config ops; auditable
		// request paths enforce their own stop-on-log-failure rule (§9).
		_ = err
	}
}

// SanitizeForAdminEvent removes secret-looking fields from change sets.
func SanitizeForAdminEvent(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch k {
		case "password", "credentials", "credentials_ciphertext", "key", "secret", "api_key",
			"access_token", "refresh_token", "password_hash", "proxy_password":
			out[k] = "[redacted]"
		default:
			out[k] = v
		}
	}
	return out
}

func NowUTC() time.Time { return time.Now().UTC() }
