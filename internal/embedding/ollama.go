package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OllamaProvider calls a local Ollama server for text embeddings.
// Ollama embed endpoint: POST /api/embeddings
type OllamaProvider struct {
	baseURL string
	model   string
	dims    int
	client  *http.Client
}

// NewOllama creates an Ollama embedding provider.
// baseURL is typically "http://localhost:11434"; model e.g. "nomic-embed-text".
func NewOllama(baseURL, model string, dims int) *OllamaProvider {
	return &OllamaProvider{
		baseURL: baseURL,
		model:   model,
		dims:    dims,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error,omitempty"`
}

// Embed returns the embedding vector for the given text using the Ollama API.
func (o *OllamaProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	payload := ollamaEmbedRequest{Model: o.model, Prompt: text}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ollama embed marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed http: %w", err)
	}
	defer resp.Body.Close()

	var result ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama embed decode: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("ollama embed api error: %s", result.Error)
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama embed: empty response")
	}
	return result.Embedding, nil
}

// EmbedBatch returns embeddings for multiple texts, calling Embed sequentially.
func (o *OllamaProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec, err := o.Embed(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("ollama embed batch[%d]: %w", i, err)
		}
		out[i] = vec
	}
	return out, nil
}

// ModelID returns the Ollama model identifier.
func (o *OllamaProvider) ModelID() string { return o.model }

// Dims returns the expected embedding dimension.
func (o *OllamaProvider) Dims() int { return o.dims }

// Ping issues a minimal embed call to verify the Ollama backend is reachable.
func (o *OllamaProvider) Ping(ctx context.Context) error {
	_, err := o.Embed(ctx, "ping")
	return err
}
