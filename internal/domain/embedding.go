package domain

import (
	"fmt"
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

// EmbeddablePrefixes are the memory-type prefixes for which an embedding is computed.
// embeddableType, partitionTypesByEmbeddable and nonEmbeddableTypeClause are all derived
// from this one list so the Go-side classification and the SQL-side complement can never
// drift apart (aihub#270). Exported so cmd/aihub-embed-backfill selects exactly the rows
// recall treats as embeddable — a prefix the two disagreed on would be a row that nothing
// embeds and nothing text-searches.
//
// Treat as read-only.
var EmbeddablePrefixes = []string{"experience.", "fact.", "rule."}

// embeddableType reports whether a memory type should have an embedding computed.
// We embed experience.*, fact.*, rule.* — the memory types an agent recalls by semantic
// similarity. We skip methodology.* because those artifacts are always fetched
// deterministically by work_item_id, so vector distance adds no value.
//
// The flip side of that choice: a methodology.* row has no emb_vector, so the vector
// recall path (whose WHERE carries `emb_vector IS NOT NULL`) can never return one. Recall
// compensates by giving the text path ownership of exactly these types — see
// partitionTypesByEmbeddable.
func embeddableType(t string) bool {
	for _, p := range EmbeddablePrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// partitionTypesByEmbeddable splits a recall type filter into the entries the vector path
// can serve and the entries only the text path can serve.
//
// A wildcard entry is classified by its prefix: "experience.*" trims to "experience.",
// which embeddableType already reports true for, so it lands with the concrete
// "experience.debug" entries. "methodology.*" trims to "methodology." and lands on the
// text side, as does any entry with no recognized prefix (e.g. a bare "rule") — which is
// correct, because a row of that exact type would never have been embedded either.
func partitionTypesByEmbeddable(types []string) (embeddable, nonEmbeddable []string) {
	for _, t := range types {
		if embeddableType(strings.TrimSuffix(t, "*")) {
			embeddable = append(embeddable, t)
		} else {
			nonEmbeddable = append(nonEmbeddable, t)
		}
	}
	return embeddable, nonEmbeddable
}

// nonEmbeddableTypeClause builds a SQL predicate matching exactly the rows embeddableType
// reports false for, binding one parameter per prefix starting at idx. It returns the
// predicate, the values to append to the arg list, and the next free placeholder index.
//
// `NOT (a OR b OR c)` is expanded to `NOT a AND NOT b AND NOT c`; memories.type is
// NOT NULL, so the negation is total and no row falls through the gap.
func nonEmbeddableTypeClause(idx int) (string, []any, int) {
	clauses := make([]string, 0, len(EmbeddablePrefixes))
	args := make([]any, 0, len(EmbeddablePrefixes))
	for _, p := range EmbeddablePrefixes {
		clauses = append(clauses, fmt.Sprintf("type NOT LIKE $%d", idx))
		args = append(args, p+"%")
		idx++
	}
	return "(" + strings.Join(clauses, " AND ") + ")", args, idx
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
