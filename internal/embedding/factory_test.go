package embedding

import (
	"testing"
	"time"
)

// innerOf strips the aihub#316 budget wrapper so the assertions below can keep
// looking at the concrete provider's fields. Defined here rather than exported
// from budget.go: production code has no reason to unwrap, only the tests do.
func innerOf(p Provider) Provider {
	if bp, ok := p.(*budgetProvider); ok {
		return bp.inner
	}
	return p
}

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantNoop  bool
		wantType  string // "openai" or "ollama" or ""
		wantErr   bool
		baseURL   string // expected baseURL for openai provider
		wantModel string
		wantDims  int
	}{
		{
			name:     "disabled — empty EMBEDDING_ENABLED returns Noop",
			env:      map[string]string{},
			wantNoop: true,
		},
		{
			name:     "disabled — EMBEDDING_ENABLED=false returns Noop",
			env:      map[string]string{"EMBEDDING_ENABLED": "false"},
			wantNoop: true,
		},
		{
			name: "openai provider with explicit base_url",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "openai",
				"EMBEDDING_BASE_URL": "http://self-hosted:8080",
				"EMBEDDING_MODEL":    "Qwen3-Embedding-8B",
				"EMBEDDING_DIMS":     "1024",
				"EMBEDDING_API_KEY":  "test-key",
			},
			wantType:  "openai",
			baseURL:   "http://self-hosted:8080",
			wantModel: "Qwen3-Embedding-8B",
			wantDims:  1024,
		},
		{
			name: "openai provider — empty base_url defaults to openai.com",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "1",
				"EMBEDDING_PROVIDER": "openai",
				"EMBEDDING_MODEL":    "text-embedding-3-small",
				"EMBEDDING_DIMS":     "1536",
				"EMBEDDING_API_KEY":  "sk-test",
			},
			wantType:  "openai",
			baseURL:   "https://api.openai.com",
			wantModel: "text-embedding-3-small",
			wantDims:  1536,
		},
		{
			name: "ollama provider",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "ollama",
				"EMBEDDING_BASE_URL": "http://localhost:11434",
				"EMBEDDING_MODEL":    "nomic-embed-text",
				"EMBEDDING_DIMS":     "768",
			},
			wantType:  "ollama",
			wantModel: "nomic-embed-text",
			wantDims:  768,
		},
		{
			name: "noop provider explicit",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "noop",
			},
			wantNoop: true,
		},
		{
			name: "unknown provider → error",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "cohere",
			},
			wantErr: true,
		},
		{
			name: "openai missing model → error",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "openai",
				"EMBEDDING_DIMS":     "1536",
				"EMBEDDING_API_KEY":  "sk-test",
			},
			wantErr: true,
		},
		{
			name: "openai missing dims → error",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "openai",
				"EMBEDDING_MODEL":    "text-embedding-3-small",
				"EMBEDDING_API_KEY":  "sk-test",
			},
			wantErr: true,
		},
		{
			name: "openai keyless (self-hosted endpoint) → ok",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "openai",
				"EMBEDDING_MODEL":    "qwen3-embedding",
				"EMBEDDING_DIMS":     "4096",
				"EMBEDDING_BASE_URL": "http://embed:8090",
			},
			wantErr: false,
		},
		{
			name: "ollama missing base_url → error",
			env: map[string]string{
				"EMBEDDING_ENABLED":  "true",
				"EMBEDDING_PROVIDER": "ollama",
				"EMBEDDING_MODEL":    "nomic-embed-text",
				"EMBEDDING_DIMS":     "768",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set env vars; track originals for cleanup.
			keys := []string{
				"EMBEDDING_ENABLED", "EMBEDDING_PROVIDER", "EMBEDDING_BASE_URL",
				"EMBEDDING_MODEL", "EMBEDDING_DIMS", "EMBEDDING_API_KEY",
				"EMBEDDING_TIMEOUT",
			}
			originals := make(map[string]string, len(keys))
			for _, k := range keys {
				originals[k] = "" // zero = "was absent"
			}
			for k := range tc.env {
				t.Setenv(k, tc.env[k])
			}
			// Unset keys not in tc.env.
			for _, k := range keys {
				if _, present := tc.env[k]; !present {
					t.Setenv(k, "")
				}
			}
			_ = originals // t.Setenv handles cleanup via t.Cleanup

			p, err := FromEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (provider=%T)", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantNoop {
				if _, ok := p.(*NoopProvider); !ok {
					t.Fatalf("expected *NoopProvider, got %T", p)
				}
				return
			}

			switch tc.wantType {
			case "openai":
				op, ok := innerOf(p).(*OpenAIProvider)
				if !ok {
					t.Fatalf("expected *OpenAIProvider, got %T", innerOf(p))
				}
				if op.baseURL != tc.baseURL {
					t.Errorf("baseURL: got %q, want %q", op.baseURL, tc.baseURL)
				}
				if op.model != tc.wantModel {
					t.Errorf("model: got %q, want %q", op.model, tc.wantModel)
				}
				if op.dims != tc.wantDims {
					t.Errorf("dims: got %d, want %d", op.dims, tc.wantDims)
				}
			case "ollama":
				op, ok := innerOf(p).(*OllamaProvider)
				if !ok {
					t.Fatalf("expected *OllamaProvider, got %T", innerOf(p))
				}
				if op.model != tc.wantModel {
					t.Errorf("model: got %q, want %q", op.model, tc.wantModel)
				}
				if op.dims != tc.wantDims {
					t.Errorf("dims: got %d, want %d", op.dims, tc.wantDims)
				}
			}
		})
	}
}

// setEmbeddingEnv sets the six construction variables plus EMBEDDING_TIMEOUT,
// clearing every key the case does not name so one subtest cannot inherit
// another's environment. Mirrors the bookkeeping inside TestFromEnv.
func setEmbeddingEnv(t *testing.T, env map[string]string) {
	t.Helper()
	keys := []string{
		"EMBEDDING_ENABLED", "EMBEDDING_PROVIDER", "EMBEDDING_BASE_URL",
		"EMBEDDING_MODEL", "EMBEDDING_DIMS", "EMBEDDING_API_KEY",
		"EMBEDDING_TIMEOUT",
	}
	for _, k := range keys {
		t.Setenv(k, env[k]) // absent from the map ⇒ ""
	}
}

// openaiEnv is a minimal, valid openai configuration; cases below overlay
// EMBEDDING_TIMEOUT on top of it.
func openaiEnv(timeout string) map[string]string {
	return map[string]string{
		"EMBEDDING_ENABLED":  "true",
		"EMBEDDING_PROVIDER": "openai",
		"EMBEDDING_BASE_URL": "http://embed.internal:8090",
		"EMBEDDING_MODEL":    "Qwen3-Embedding-8B",
		"EMBEDDING_DIMS":     "1024",
		"EMBEDDING_TIMEOUT":  timeout,
	}
}

// TestFromEnv_AppliesBudget is the test that proves the SERVER gets the bound,
// not merely that the decorator works. Without it the decorator could be
// perfect and never wired in — which is the failure mode aihub#316 is about:
// three correct fallbacks that never got to run.
func TestFromEnv_AppliesBudget(t *testing.T) {
	t.Run("openai is budget-wrapped with the default", func(t *testing.T) {
		setEmbeddingEnv(t, openaiEnv(""))
		p, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		budget, bounded := BudgetOf(p)
		if !bounded {
			t.Fatalf("FromEnv returned an unbounded provider %T — the server would inherit the 30s failure", p)
		}
		if budget != DefaultBudget {
			t.Errorf("budget = %s, want DefaultBudget %s", budget, DefaultBudget)
		}
		if _, ok := innerOf(p).(*OpenAIProvider); !ok {
			t.Errorf("wrapped provider is %T, want *OpenAIProvider", innerOf(p))
		}
	})

	t.Run("ollama is budget-wrapped", func(t *testing.T) {
		setEmbeddingEnv(t, map[string]string{
			"EMBEDDING_ENABLED":  "true",
			"EMBEDDING_PROVIDER": "ollama",
			"EMBEDDING_BASE_URL": "http://localhost:11434",
			"EMBEDDING_MODEL":    "nomic-embed-text",
			"EMBEDDING_DIMS":     "768",
		})
		p, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if _, bounded := BudgetOf(p); !bounded {
			t.Fatalf("FromEnv returned an unbounded provider %T", p)
		}
	})

	// The trap guard, at the seam rather than on the decorator: main.go hands
	// FromEnv's result straight to domain.InitEmbeddingProvider, and
	// domain.isNoopProvider type-asserts on *NoopProvider. A wrapper here would
	// turn every "embedding disabled" deployment onto the vector path.
	t.Run("noop provider stays a bare *NoopProvider", func(t *testing.T) {
		for _, env := range []map[string]string{
			{"EMBEDDING_ENABLED": "true", "EMBEDDING_PROVIDER": "noop", "EMBEDDING_TIMEOUT": "5s"},
			{"EMBEDDING_ENABLED": "false", "EMBEDDING_TIMEOUT": "5s"},
		} {
			setEmbeddingEnv(t, env)
			p, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv(%v): %v", env, err)
			}
			if _, ok := p.(*NoopProvider); !ok {
				t.Errorf("FromEnv(%v) = %T, want a bare *NoopProvider", env, p)
			}
		}
	})
}

// TestFromEnv_EmbeddingTimeoutParsing covers the new env var's three outcomes.
func TestFromEnv_EmbeddingTimeoutParsing(t *testing.T) {
	tests := []struct {
		name        string
		timeout     string
		wantBounded bool
		wantBudget  time.Duration
	}{
		{"valid duration is honoured", "800ms", true, 800 * time.Millisecond},
		{"valid seconds duration is honoured", "12s", true, 12 * time.Second},
		{"surrounding whitespace is tolerated", "  2s  ", true, 2 * time.Second},
		{"garbage falls back to the default", "soon", true, DefaultBudget},
		{"a bare integer is garbage (no unit) and falls back", "5", true, DefaultBudget},
		{"unset keeps the default", "", true, DefaultBudget},
		{"zero disables the bound", "0", false, 0},
		{"negative disables the bound", "-1s", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setEmbeddingEnv(t, openaiEnv(tc.timeout))
			p, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
			budget, bounded := BudgetOf(p)
			if bounded != tc.wantBounded {
				t.Fatalf("EMBEDDING_TIMEOUT=%q: bounded=%v (provider %T), want %v",
					tc.timeout, bounded, p, tc.wantBounded)
			}
			if bounded && budget != tc.wantBudget {
				t.Errorf("EMBEDDING_TIMEOUT=%q: budget=%s, want %s", tc.timeout, budget, tc.wantBudget)
			}
			if !bounded {
				if _, ok := p.(*OpenAIProvider); !ok {
					t.Errorf("EMBEDDING_TIMEOUT=%q: unwrapped provider is %T, want *OpenAIProvider", tc.timeout, p)
				}
			}
		})
	}
}
