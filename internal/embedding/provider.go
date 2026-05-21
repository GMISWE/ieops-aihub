// Package embedding provides text embedding generation for the memory recall system.
package embedding

import "context"

// Provider is the interface for text embedding generation.
// Implementations: OpenAIProvider, OllamaProvider, NoopProvider.
type Provider interface {
	// Embed returns the embedding vector for a single text string.
	Embed(ctx context.Context, text string) ([]float32, error)
	// EmbedBatch returns embedding vectors for multiple texts (may call Embed individually).
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	// ModelID returns the model identifier stored in memories.emb_model.
	ModelID() string
	// Dims returns the embedding dimension stored in memories.emb_dims.
	Dims() int
	// Ping verifies the embedding backend is reachable.
	Ping(ctx context.Context) error
}

// NoopProvider returns nil embeddings. Used when no embedding backend is configured.
// Recall will fall back to text/tag search when embeddings are unavailable.
type NoopProvider struct{}

func (n *NoopProvider) Embed(_ context.Context, _ string) ([]float32, error) { return nil, nil }

func (n *NoopProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return make([][]float32, len(texts)), nil
}

func (n *NoopProvider) ModelID() string             { return "" }
func (n *NoopProvider) Dims() int                   { return 0 }
func (n *NoopProvider) Ping(_ context.Context) error { return nil }
