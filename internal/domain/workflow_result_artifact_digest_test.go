package domain

// workflow_result_artifact_digest_test.go — the pure pin for the workflow
// artifact digest (aihub#708 final Astra blocker 2). The digest rule has TWO
// implementations by design (the domain's record-time recomputation here, and
// internal/controller's client-side WorkflowArtifactHash, which authored the
// convention) and no shared home: internal/controller sits on pkg/client and
// is not importable from this package. What keeps the two from drifting is
// each side pinning the same fixed literals — sha256 over encoding/json's
// deterministic (key-sorted) serialization of the structured payload. The
// values below were computed independently of the Go code (printf | sha256sum
// over the canonical bytes), so a change to the rule — a different serializer,
// a different key order, a different prefix — is a red test, not a silent
// divergence between what the controller hashes and what the server accepts.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowArtifactDigestPinsTheCanonicalSerialization(t *testing.T) {
	require.Equal(t,
		"sha256:f41c36c7c4dce494cbcb4f346de37fd16be80cb33240c9299c75821f24a1225c",
		workflowArtifactDigest(map[string]any{"summary": "artifact pin"}),
		"sha256 over the key-sorted encoding/json serialization of {\"summary\":\"artifact pin\"}")

	// Two keys in non-sorted insertion order: encoding/json sorts them, so
	// the digest is over {\"a\":\"x\",\"b\":2} — the byte-identical answer a
	// controller-side caller hashing the same value tree computes.
	require.Equal(t,
		"sha256:768ca668c0f84dd39bf269e25c9a3f0af4812e41026b6fead9a2666078ef16f6",
		workflowArtifactDigest(map[string]any{"b": 2, "a": "x"}),
		"map insertion order must not leak into the digest")

	// The empty payload is a legal digest too (an empty structured output
	// object), and it is NOT the digest of nothing: "{}" hashes, not "".
	require.Equal(t,
		"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
		workflowArtifactDigest(map[string]any{}),
		"the empty object serializes to {} and hashes as such")
}
