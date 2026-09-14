package accounts

// OpenAIPlatformModels is the platform fallback catalog used by Sub2API,
// backend/internal/pkg/openai/constants.go (2026-09-14). A catalog entry is
// not evidence that an individual OAuth account can execute that model.
// Keep it separate from account_model_capabilities and from billing prices.
var OpenAIPlatformModels = []string{
	"gpt-5.6-sol", "gpt-6", "gpt-5.6", "gpt-5.6-terra", "gpt-5.6-luna",
	"gpt-6-astra", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark",
	"codex-auto-review", "gpt-5.2", "gpt-image-1", "gpt-image-1.5", "gpt-image-2",
	"gpt-image-2.5-flare", "gpt-image-2.5-sunburst",
}
