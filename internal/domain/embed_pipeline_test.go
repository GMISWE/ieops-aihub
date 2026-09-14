package domain

import (
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// aihub#661 acceptance, first half: the stamp must carry every dimension that
// can put two vectors in different spaces. Each arm below moves ONE input and
// requires the stamp to move with it, so dropping a field from the format goes
// red on exactly the field that was dropped — a single "the stamp looks right"
// assertion would go green the moment a field is silently omitted.
//
// The second half (a pipeline change must put the old rows into the backfill's
// re-embed set) lives in cmd/aihub-embed-backfill/main_test.go, because that is
// where the predicate is.

// withBudget swaps the package-level input budget for one test. It is a package
// var resolved once at init from EMBEDDING_INPUT_MAX_RUNES on purpose (see
// embed_input.go), so there is no setter to call and a test in this package
// assigns it directly.
func withBudget(t *testing.T, n int) {
	t.Helper()
	prev := embedInputMaxRunes
	embedInputMaxRunes = n
	t.Cleanup(func() { embedInputMaxRunes = prev })
}

func TestEmbedPipelineIDMovesWithEveryDimension(t *testing.T) {
	withBudget(t, 16000)
	t.Setenv("EMBEDDING_SERVING_ID", "tei-1.9.3-lasttoken-causal")

	base := EmbedPipelineID("Qwen/Qwen3-Embedding-0.6B", 1024)

	t.Run("model", func(t *testing.T) {
		if got := EmbedPipelineID("BAAI/bge-m3", 1024); got == base {
			t.Errorf("stamp does not carry the model: %q for two different models", got)
		}
	})

	t.Run("dims", func(t *testing.T) {
		if got := EmbedPipelineID("Qwen/Qwen3-Embedding-0.6B", 512); got == base {
			t.Errorf("stamp does not carry the dimensions: %q for two different widths", got)
		}
	})

	t.Run("input budget", func(t *testing.T) {
		withBudget(t, 6000)
		if got := EmbedPipelineID("Qwen/Qwen3-Embedding-0.6B", 1024); got == base {
			t.Errorf("stamp does not carry the input budget: %q at 16000 and at 6000 runes", got)
		}
	})

	// The dimension aihub#648 actually tripped over: same checkpoint, same
	// width, same budget, different SERVING — bidirectional vs causal
	// attention, measured cosine 0.139-0.348 between the two spaces.
	t.Run("serving identity", func(t *testing.T) {
		t.Setenv("EMBEDDING_SERVING_ID", "tei-1.7.2-lasttoken-bidirectional")
		if got := EmbedPipelineID("Qwen/Qwen3-Embedding-0.6B", 1024); got == base {
			t.Errorf("stamp does not carry the serving identity: %q under 1.7.2 and 1.9.3", got)
		}
	})

	// The query composition (aihub#669's instruct prefix) is a dimension of the
	// stamp even though it does not drive re-embedding — see the doc/query split
	// asserted below.
	t.Run("query composition", func(t *testing.T) {
		a := queryPrefixFingerprint(QueryEmbedPrefix)
		b := queryPrefixFingerprint(QueryEmbedPrefix + " ")
		if a == b {
			t.Errorf("query fingerprint does not carry the prefix: %q for two different prefixes", a)
		}
		if !strings.Contains(base, a) {
			t.Errorf("stamp %q does not carry the query fingerprint %q", base, a)
		}
	})
}

// The undeclared serving state must be a VALUE in the data, not a silence.
// aihub#661 exists because a serving swap left no trace anywhere; a stamp that
// simply omitted the field when nobody declared one would read exactly like a
// declared pipeline and reproduce that.
func TestUndeclaredServingIsWrittenIntoTheStamp(t *testing.T) {
	withBudget(t, 16000)
	t.Setenv("EMBEDDING_SERVING_ID", "")

	stamp := EmbedPipelineID("Qwen/Qwen3-Embedding-0.6B", 1024)
	if !strings.Contains(stamp, "s="+embedding.ServingUndeclared) {
		t.Errorf("stamp %q does not record that the serving side was undeclared", stamp)
	}
	// The LIKE pattern serving_id.go promises an operator can select on.
	if !strings.Contains(stamp, ";s="+embedding.ServingUndeclared+embedding.StampSegmentSep) {
		t.Errorf("stamp %q does not match the documented `%%;s=undeclared|%%` selector", stamp)
	}
}

// The doc/query split, which is what lets the stamp carry the query composition
// without making a query-prompt change re-embed the whole corpus. Qwen3-Embedding
// ships prompts.document = "" (embed_input.go, measured in aihub#660), so a
// query-side change provably moves no stored vector.
func TestQueryCompositionIsOutsideTheConvergenceSegment(t *testing.T) {
	withBudget(t, 16000)
	t.Setenv("EMBEDDING_SERVING_ID", "tei-1.9.3-lasttoken-causal")

	stamp := EmbedPipelineID("Qwen/Qwen3-Embedding-0.6B", 1024)
	doc := EmbedPipelineDoc("Qwen/Qwen3-Embedding-0.6B", 1024)

	seg, _, found := strings.Cut(stamp, embedding.StampSegmentSep)
	if !found {
		t.Fatalf("stamp %q has no segment separator — the backfill's split_part would compare the whole stamp", stamp)
	}
	if seg != doc {
		t.Errorf("first segment %q is not EmbedPipelineDoc %q; the backfill compares the first segment", seg, doc)
	}
	if strings.Contains(doc, "qry:") {
		t.Errorf("convergence segment %q carries the query composition — a prefix change would re-embed the corpus for nothing", doc)
	}
	if !strings.Contains(stamp, "qry:") {
		t.Errorf("stamp %q does not record the query composition at all", stamp)
	}
}

// A free-text operator value must not be able to forge a segment or field
// boundary: the backfill compares split_part(emb_pipeline, '|', 1), so a
// serving id containing '|' would make it compare a truncated string and two
// different pipelines could land on the same comparison key.
func TestOperatorValuesCannotForgeStampStructure(t *testing.T) {
	withBudget(t, 16000)
	t.Setenv("EMBEDDING_SERVING_ID", "tei|1.9.3;causal")

	doc := EmbedPipelineDoc("Qwen/Qwen3-Embedding-0.6B", 1024)
	if strings.Contains(doc, embedding.StampSegmentSep) {
		t.Errorf("serving id forged a segment boundary into the convergence segment: %q", doc)
	}
	if strings.Count(doc, embedding.StampFieldSep) != 3 {
		t.Errorf("serving id forged a field boundary: %q has %d field separators, want 3 (m;d;in;s)",
			doc, strings.Count(doc, embedding.StampFieldSep))
	}

	// A model name is operator-supplied too (EMBEDDING_MODEL).
	if got := EmbedPipelineDoc("we|ird;model", 1024); strings.Contains(got, embedding.StampSegmentSep) {
		t.Errorf("model name forged a segment boundary: %q", got)
	}
}
