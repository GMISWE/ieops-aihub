package embedding

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// FromEnv constructs an embedding Provider from environment variables.
//
// Variables:
//
//	EMBEDDING_ENABLED   — "true" or "1" to enable; anything else → NoopProvider
//	EMBEDDING_PROVIDER  — "openai" | "ollama" | "noop" (required when enabled)
//	EMBEDDING_BASE_URL  — base URL for the embedding server
//	EMBEDDING_MODEL     — model name (required for openai/ollama)
//	EMBEDDING_DIMS      — output dimension as integer (required for openai/ollama)
//	EMBEDDING_API_KEY   — API key (optional; omit for keyless self-hosted
//	                      OpenAI-compatible endpoints like llama.cpp/Ollama/vLLM)
//	EMBEDDING_TIMEOUT   — per-call embedding budget as a Go duration string
//	                      (e.g. "5s", "800ms"); unset or unparseable keeps
//	                      DefaultBudget. "0" or a negative value disables the
//	                      bound — the escape hatch for bulk tooling that is not
//	                      serving an HTTP request (aihub#316)
//
// One more EMBEDDING_* variable is read outside this constructor, because it
// governs the text handed to a provider rather than the provider itself:
//
//	EMBEDDING_INPUT_MAX_RUNES
//	                    — rune budget applied to memory/work-item text before
//	                      embedding; unset or unparseable keeps
//	                      DefaultInputMaxRunes (input_budget.go). Unlike
//	                      EMBEDDING_TIMEOUT, "0" does NOT disable it — see
//	                      InputMaxRunes for why that asymmetry is deliberate.
//	                      Consumed by domain.MemoryEmbedInput /
//	                      domain.WorkItemEmbedInput, so it moves every writer
//	                      at once (aihub#361).
//
// The openai and ollama providers are returned budget-wrapped (budget.go). The
// wiring lives here rather than at the call site in cmd/aihub/main.go because
// FromEnv is the single construction point: a test of FromEnv then proves the
// running server actually gets the bound, which a test of the decorator alone
// could never show.
func FromEnv() (Provider, error) {
	enabled := os.Getenv("EMBEDDING_ENABLED")
	if enabled != "true" && enabled != "1" {
		return &NoopProvider{}, nil
	}

	provider := os.Getenv("EMBEDDING_PROVIDER")
	baseURL := os.Getenv("EMBEDDING_BASE_URL")
	model := os.Getenv("EMBEDDING_MODEL")
	dimsStr := os.Getenv("EMBEDDING_DIMS")
	apiKey := os.Getenv("EMBEDDING_API_KEY")

	switch provider {
	case "openai":
		if model == "" {
			return nil, fmt.Errorf("embedding: EMBEDDING_MODEL required for openai provider")
		}
		if dimsStr == "" {
			return nil, fmt.Errorf("embedding: EMBEDDING_DIMS required for openai provider")
		}
		dims, err := strconv.Atoi(dimsStr)
		if err != nil {
			return nil, fmt.Errorf("embedding: EMBEDDING_DIMS must be an integer: %w", err)
		}
		// apiKey is optional: keyless self-hosted endpoints (llama.cpp/Ollama/vLLM)
		// ignore the Authorization header; only real api.openai.com needs it.
		return WithBudget(NewOpenAI(apiKey, model, dims, baseURL), budgetFromEnv()), nil

	case "ollama":
		if model == "" {
			return nil, fmt.Errorf("embedding: EMBEDDING_MODEL required for ollama provider")
		}
		if dimsStr == "" {
			return nil, fmt.Errorf("embedding: EMBEDDING_DIMS required for ollama provider")
		}
		dims, err := strconv.Atoi(dimsStr)
		if err != nil {
			return nil, fmt.Errorf("embedding: EMBEDDING_DIMS must be an integer: %w", err)
		}
		if baseURL == "" {
			return nil, fmt.Errorf("embedding: EMBEDDING_BASE_URL required for ollama provider")
		}
		return WithBudget(NewOllama(baseURL, model, dims), budgetFromEnv()), nil

	case "noop", "":
		// Deliberately bare: domain.isNoopProvider is a type assertion on
		// *NoopProvider, so handing back a wrapper here would flip every
		// "embedding is disabled" route onto the vector path (aihub#316).
		return &NoopProvider{}, nil

	default:
		return nil, fmt.Errorf("embedding: unknown provider %q (want openai|ollama|noop)", provider)
	}
}

// budgetFromEnv reads EMBEDDING_TIMEOUT into a per-call embedding budget.
//
// An unparseable value keeps DefaultBudget rather than failing startup: this
// bound is a safety net, and refusing to boot over a typo in it would turn a
// degraded-quality knob into an outage. It is logged so the typo is findable.
// A parsed "0" or negative value is honoured as "no bound" (see WithBudget).
func budgetFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("EMBEDDING_TIMEOUT"))
	if raw == "" {
		return DefaultBudget
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"warn: EMBEDDING_TIMEOUT=%q is not a Go duration (%v) — using %s\n", raw, err, DefaultBudget)
		return DefaultBudget
	}
	return d
}
