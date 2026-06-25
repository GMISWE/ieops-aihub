package embedding

import (
	"fmt"
	"os"
	"strconv"
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
		return NewOpenAI(apiKey, model, dims, baseURL), nil

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
		return NewOllama(baseURL, model, dims), nil

	case "noop", "":
		return &NoopProvider{}, nil

	default:
		return nil, fmt.Errorf("embedding: unknown provider %q (want openai|ollama|noop)", provider)
	}
}
