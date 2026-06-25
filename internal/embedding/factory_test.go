package embedding

import (
	"testing"
)

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
				op, ok := p.(*OpenAIProvider)
				if !ok {
					t.Fatalf("expected *OpenAIProvider, got %T", p)
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
				op, ok := p.(*OllamaProvider)
				if !ok {
					t.Fatalf("expected *OllamaProvider, got %T", p)
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
