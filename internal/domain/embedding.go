package domain

import (
	"strconv"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// embProvider is the package-level embedding provider.
// Defaults to NoopProvider (returns nil vectors) until InitEmbeddingProvider is called.
// Writes from Remember and reads from RecallWithVector use this.
var embProvider embedding.Provider = &embedding.NoopProvider{}

// InitEmbeddingProvider sets the package-level embedding provider.
// Call once from cmd/aihub/main.go before serving requests.
func InitEmbeddingProvider(p embedding.Provider) {
	embProvider = p
}

// embeddableType reports whether a memory type should have an embedding computed.
// We embed experience.*, fact.*, rule.* — the memory types an agent recalls by semantic
// similarity. We skip methodology.* because those artifacts are always fetched
// deterministically by work_item_id, so vector distance adds no value.
func embeddableType(t string) bool {
	return strings.HasPrefix(t, "experience.") ||
		strings.HasPrefix(t, "fact.") ||
		strings.HasPrefix(t, "rule.")
}

// vecToPGLiteral encodes a float32 slice as a pgvector literal string, e.g. "[0.1,0.2,...]".
// pgx/v5 does not know the VECTOR type, so we pass the value as a text literal and cast
// it in SQL with $N::vector.
func vecToPGLiteral(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
