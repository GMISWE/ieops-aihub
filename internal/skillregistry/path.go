// Package skillregistry holds the pure, database-free halves of the versioned
// skill registry: the closed file bundle (entry/files/provenance/license), the
// runtime capability contract, the content digest, and the fail-closed JSON
// Schema subset those contracts may use.
//
// The package exists so those rules have exactly one home, shared by the
// domain entrypoints that store skill versions (internal/domain) and by later
// batches that materialize, import or execute them (Batch 2's flow validation,
// Batch 4A's idempotent seed/import). Everything here is pure: no SQL, no
// caller identity, no visibility decisions. Authorization lives in
// internal/domain.
//
// Two invariants carry the registry's security posture and both are enforced
// here, at the boundary where untrusted bytes first become a skill:
//
//   - CLOSED BUNDLE (spec D2): a bundle's file set is exactly the `files`
//     array, the entry point is one of those files, and decoding rejects
//     unknown keys — so no bundle can smuggle a "fetch me at runtime"
//     instruction, a symlink target, or an out-of-band reference. Skill
//     source artifacts are immutable data, never an authorization source.
//
//   - FAIL-CLOSED SCHEMAS (spec D6): a contract schema is accepted only if
//     every keyword in it is one this package implements. A keyword outside
//     the subset is refused with the keyword named, rather than validated
//     with a partial JSON Schema implementation that silently ignores it.
package skillregistry

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Bundle path limits. Kept deliberately small: a bundle is a prompt-plus-few
// supporting files, not a repo; and the JSONB column is inline storage, not
// large-object storage.
const (
	// MaxPathBytes is the cap on one bundled file path.
	MaxPathBytes = 255
	// MaxPathComponentBytes is the cap on one path component. 128 is the
	// common filesystem ceiling (ext4 is 255 per component; we stay under it
	// with headroom for materialization prefixes).
	MaxPathComponentBytes = 128
)

// ValidateBundlePath reports whether p is a NORMALIZED RELATIVE bundle path —
// the one form D2 allows a bundled file to carry. The rules, and why each
// rejects what it rejects:
//
//   - non-empty, at most MaxPathBytes, and valid UTF-8 — a path is stored and
//     compared as a string, so invalid UTF-8 would compare differently on the
//     PostgreSQL side than in Go;
//   - no control characters (including NUL) anywhere — materializing a bundle
//     to disk must never need shell quoting to survive;
//   - no backslash, no Windows drive prefix ("C:"), no leading "/" and no
//     leading "~" — the path must be relative and platform-neutral, because
//     the same stored bytes are materialized on Linux CI and read on other
//     hosts;
//   - "/"-separated segments, each non-empty (so no "//"), none equal to "." or
//     ".." (so no traversal), and none consisting only of dots — "..." is
//     rejected with ".." because a component of only dots carries no meaning a
//     real file needs and every meaning it could carry is hostile;
//   - no trailing "/" — a path that names a file must not look like a
//     directory.
//
// The accepted set is exactly the normalized set: anything that WOULD need
// normalizing is rejected instead, so a stored path can be used to build a
// filesystem name directly, with no cleaning step whose absence would be the
// vulnerability.
func ValidateBundlePath(p string) error {
	if p == "" {
		return fmt.Errorf("bundle path is empty")
	}
	if len(p) > MaxPathBytes {
		return fmt.Errorf("bundle path %q exceeds %d bytes", p, MaxPathBytes)
	}
	if !utf8.ValidString(p) {
		return fmt.Errorf("bundle path %q is not valid UTF-8", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("bundle path %q is absolute; bundle paths must be relative", p)
	}
	if strings.HasPrefix(p, "~") {
		return fmt.Errorf("bundle path %q starts with ~; bundle paths must not name home-relative paths", p)
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("bundle path %q contains a backslash; bundle paths use '/' separators", p)
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("bundle path %q ends with '/'; a bundle path names a file", p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("bundle path %q contains a control character", p)
		}
	}
	// A Windows drive prefix would be interpreted as absolute by any host that
	// understands it; on the hosts that do not, "C:foo" is a legal-looking
	// relative path that names a different file depending on the reader.
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return fmt.Errorf("bundle path %q has a drive-letter prefix; bundle paths must be relative", p)
	}
	segments := strings.Split(p, "/")
	for _, seg := range segments {
		if seg == "" {
			return fmt.Errorf("bundle path %q has an empty segment", p)
		}
		if len(seg) > MaxPathComponentBytes {
			return fmt.Errorf("bundle path %q segment %q exceeds %d bytes", p, seg, MaxPathComponentBytes)
		}
		if strings.Trim(seg, ".") == "" {
			return fmt.Errorf("bundle path %q segment %q consists only of dots", p, seg)
		}
	}
	return nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
