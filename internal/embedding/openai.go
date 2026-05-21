package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OpenAIProvider calls the OpenAI Embeddings API.
type OpenAIProvider struct {
	apiKey  string
	model   string
	dims    int
	baseURL string
	client  *http.Client
}

// NewOpenAI creates an OpenAI embedding provider.
// model is typically "text-embedding-3-small"; dims is the output dimension (e.g. 1536).
func NewOpenAI(apiKey, model string, dims int) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		dims:    dims,
		baseURL: "https://api.openai.com",
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

type openAIEmbedRequest struct {
	Input      string `json:"input"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Embed returns the embedding vector for the given text.
func (o *OpenAIProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	payload := openAIEmbedRequest{Input: text, Model: o.model, Dimensions: o.dims}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai embed marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai embed request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed http: %w", err)
	}
	defer resp.Body.Close()

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("openai embed decode: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("openai embed api error: %s (%s)", result.Error.Message, result.Error.Type)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("openai embed: empty response")
	}
	return result.Data[0].Embedding, nil
}

// EmbedBatch returns embeddings for multiple texts, calling Embed sequentially.
func (o *OpenAIProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec, err := o.Embed(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("openai embed batch[%d]: %w", i, err)
		}
		out[i] = vec
	}
	return out, nil
}

// ModelID returns the OpenAI model identifier.
func (o *OpenAIProvider) ModelID() string { return o.model }

// Dims returns the embedding dimension.
func (o *OpenAIProvider) Dims() int { return o.dims }

// Ping issues a minimal embed call to verify the backend is reachable.
func (o *OpenAIProvider) Ping(ctx context.Context) error {
	_, err := o.Embed(ctx, "ping")
	return err
}
