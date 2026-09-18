package domain

// workflow_digest_test.go — the DB-free half of the digest validation the
// result path applies (aihub#708 review blocker: "digest suffix not hex").
//
// isSHA256Digest must DECODE the 64-character suffix, not merely check its
// prefix and length: a suffix of non-hex bytes passed the old shape check and
// stored a digest no consumer could ever recompute against an artifact. It
// must also hold the pure package's lowercase vocabulary
// (internal/workflow's sha256RE), so uppercase hex decodes but is still
// refused — the DB CHECK constraints and the pure policy agree on lowercase.
//
// No database needed:
//
//	GOWORK=off go test ./internal/domain/ -run TestWorkflowDigest -v -count=1

import (
	"strings"
	"testing"
)

func TestWorkflowDigestSuffixMustDecodeHex(t *testing.T) {
	valid := "sha256:" + strings.Repeat("0123456789abcdef", 4)
	if !isSHA256Digest(valid) {
		t.Fatal("a full lowercase-hex sha256 digest must validate")
	}
	for name, bad := range map[string]string{
		"non-hex suffix":  "sha256:" + strings.Repeat("z", 64),
		"uppercase hex":   "sha256:" + strings.Repeat("ABCDEF", 10) + "AbCd",
		"mixed garbage":   "sha256:" + strings.Repeat("0x", 32),
		"short suffix":    "sha256:abcd",
		"no prefix":       strings.Repeat("a", 64),
		"no suffix":       "sha256:",
		"truncated hex":   valid[:len(valid)-1],
		"wrong algorithm": "sha512:" + strings.Repeat("ab", 64),
	} {
		if isSHA256Digest(bad) {
			t.Fatalf("%s passed the digest check: %q", name, bad)
		}
	}
}
