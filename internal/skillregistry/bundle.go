package skillregistry

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Bundle size limits. These bound a skill version to prompt-scale content: a
// JSONB column is inline row storage, and an unbounded bundle would let one
// publish bloat every row scan of skill_versions. They are enforced at decode
// time so the refusal is a 400 naming the limit, not a database error.
const (
	// MaxBundleFiles caps the number of files in one bundle.
	MaxBundleFiles = 256
	// MaxFileBytes caps one file's content (the decoded content for base64).
	MaxFileBytes = 512 * 1024
	// MaxBundleBytes caps the whole serialized bundle.
	MaxBundleBytes = 2 * 1024 * 1024
)

// EncodingFileUTF8 and EncodingFileBase64 are the only file content encodings
// a bundle accepts. Binary content travels as base64; everything else is text.
// The closed set exists so materialization never has to guess.
const (
	EncodingFileUTF8   = "utf-8"
	EncodingFileBase64 = "base64"
)

// SkillFile is one file inside a skill bundle. The struct is CLOSED: decoding
// with DisallowUnknownFields (DecodeBundle) refuses any other key, which is
// what keeps a bundle from carrying a `url`, `ref` or `symlink_to` field that
// a materializer might one day obey — D2's "no dynamic upstream fetch" and
// "no symlinks" rules are enforced by there being no field to put them in.
type SkillFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"` // "" or EncodingFileUTF8 → text; EncodingFileBase64 → base64
}

// BundleProvenance records where a bundle came from. All fields are optional:
// a skill authored directly in aihub has an empty provenance, an imported one
// carries its upstream identity so the MIT notice requirement (D2) has a place
// to be satisfied.
type BundleProvenance struct {
	Source          string `json:"source,omitempty"`           // e.g. "polyforge/skills/grill-me"
	UpstreamURL     string `json:"upstream_url,omitempty"`     // where the bytes were imported from
	UpstreamCommit  string `json:"upstream_commit,omitempty"`  // commit the import snapshotted
	UpstreamLicense string `json:"upstream_license,omitempty"` // SPDX id of the upstream license
	ImportedAt      string `json:"imported_at,omitempty"`      // RFC3339
	Notes           string `json:"notes,omitempty"`
}

// BundleLicense is the license of the bundled content itself. Name is an SPDX
// id ("MIT", "Apache-2.0", "Proprietary") and is required: every stored skill
// version must carry a license, even if the value is "Proprietary". Notice
// carries the upstream license text when one applies (D2: preserve upstream MIT
// notice).
type BundleLicense struct {
	Name   string `json:"name"`
	Notice string `json:"notice,omitempty"`
	URL    string `json:"url,omitempty"`
}

// SkillBundle is the closed file bundle stored in skill_versions.bundle.
// Entry names the file execution starts from, Files is the whole file set, and
// Entry must be exactly one of the Files paths.
type SkillBundle struct {
	Entry      string           `json:"entry"`
	Files      []SkillFile      `json:"files"`
	Provenance BundleProvenance `json:"provenance,omitempty"`
	License    BundleLicense    `json:"license"`
}

// DecodeBundle parses raw bundle JSON into a SkillBundle, strictly: unknown
// keys anywhere in the object are refused (the closed-bundle rule), and the
// result is fully validated (ValidateBundle) before it is returned. Callers
// that then store json.Marshal(bundle) store EXACTLY what was validated —
// there is no window where a bundle is stored in a shape ValidateBundle never
// saw.
func DecodeBundle(raw []byte) (*SkillBundle, error) {
	if len(raw) == 0 {
		return nil, errors.New("bundle is empty")
	}
	var b SkillBundle
	if _, err := decodeStrictOne(raw, &b, "bundle", bundleSupportedKeys); err != nil {
		return nil, err
	}
	if err := ValidateBundle(&b); err != nil {
		return nil, err
	}
	return &b, nil
}

// bundleSupportedKeys lists every key the closed bundle vocabulary accepts,
// for decodeStrictOne's refusal message.
var bundleSupportedKeys = []string{
	"entry", "files", "files[].path", "files[].content", "files[].encoding",
	"provenance", "provenance.source", "provenance.upstream_url",
	"provenance.upstream_commit", "provenance.upstream_license",
	"provenance.imported_at", "provenance.notes",
	"license", "license.name", "license.notice", "license.url",
}

// ValidateBundle enforces every closed-bundle rule:
//
//   - entry is a valid normalized relative path and names exactly one of the
//     files (so execution can never start outside the bundle);
//   - the file list is non-empty and within MaxBundleFiles, every path is a
//     valid normalized relative path, and no two files share a path (two files
//     at one path would make the bundle's content depend on materialization
//     order);
//   - no file path is an ANCESTOR DIRECTORY of another file's path: "a" and
//     "a/SKILL.md" cannot coexist, in either order — a filesystem cannot hold
//     a file and a directory at one name, so a bundle carrying both is
//     unmaterializable and is refused at validation time, not at extraction;
//   - every file's encoding is one of the two known values and its content
//     fits MaxFileBytes (decoded, for base64) and decodes cleanly;
//   - the whole bundle serializes within MaxBundleBytes;
//   - the license has a non-empty SPDX-style name.
//
// It is called by DecodeBundle and is exported for callers that build a bundle
// in Go (seed/import) and want the same rules on their own struct.
func ValidateBundle(b *SkillBundle) error {
	if b == nil {
		return errors.New("bundle is empty")
	}
	if b.Entry == "" {
		return errors.New("bundle entry is empty")
	}
	if err := ValidateBundlePath(b.Entry); err != nil {
		return fmt.Errorf("bundle entry: %w", err)
	}
	if len(b.Files) == 0 {
		return errors.New("bundle has no files")
	}
	if len(b.Files) > MaxBundleFiles {
		return fmt.Errorf("bundle has %d files; the limit is %d", len(b.Files), MaxBundleFiles)
	}
	seen := make(map[string]bool, len(b.Files))
	// Ancestor directories each file needs: prefix path -> a file that needs
	// it as a directory. Built over ALL files first, so the collision check
	// below is symmetric — which of "a" and "a/SKILL.md" was listed first
	// cannot matter.
	ancestorDirOf := make(map[string]string, len(b.Files))
	entryFound := false
	for i := range b.Files {
		f := &b.Files[i]
		if f.Path == "" {
			return fmt.Errorf("bundle file %d has an empty path", i)
		}
		if err := ValidateBundlePath(f.Path); err != nil {
			return fmt.Errorf("bundle file %d: %w", i, err)
		}
		if seen[f.Path] {
			return fmt.Errorf("bundle file %d duplicates path %q", i, f.Path)
		}
		seen[f.Path] = true
		if rest := f.Path; strings.LastIndexByte(rest, '/') > 0 {
			for {
				cut := strings.LastIndexByte(rest, '/')
				if cut <= 0 {
					break
				}
				rest = rest[:cut]
				if _, ok := ancestorDirOf[rest]; !ok {
					ancestorDirOf[rest] = f.Path
				}
			}
		}
		if f.Path == b.Entry {
			entryFound = true
		}
		switch f.Encoding {
		case "", EncodingFileUTF8:
			if len(f.Content) > MaxFileBytes {
				return fmt.Errorf("bundle file %q exceeds %d bytes", f.Path, MaxFileBytes)
			}
		case EncodingFileBase64:
			decoded, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				return fmt.Errorf("bundle file %q: base64 content does not decode: %w", f.Path, err)
			}
			if len(decoded) > MaxFileBytes {
				return fmt.Errorf("bundle file %q exceeds %d bytes decoded", f.Path, MaxFileBytes)
			}
		default:
			return fmt.Errorf("bundle file %q has unknown encoding %q (supported: %q, %q)",
				f.Path, f.Encoding, EncodingFileUTF8, EncodingFileBase64)
		}
	}
	// The ancestor collision check: a file whose path is also a directory
	// prefix another file needs ("a" vs "a/SKILL.md") is unmaterializable.
	// Both orders are caught because ancestorDirOf was built over the whole
	// file list before this loop reads it.
	for i := range b.Files {
		f := &b.Files[i]
		if needsIt, ok := ancestorDirOf[f.Path]; ok {
			return fmt.Errorf("bundle file %q collides with bundle file %q: one path is the other's parent directory", f.Path, needsIt)
		}
	}
	if !entryFound {
		return fmt.Errorf("bundle entry %q is not one of the bundle's files", b.Entry)
	}
	if name := b.License.Name; name == "" {
		return errors.New("bundle license name is empty")
	}
	// The serialized-size check runs on the canonical form: what would be
	// stored. A bundle that fits in the request only because of exotic
	// whitespace is still refused.
	if canonical, err := json.Marshal(b); err != nil {
		return fmt.Errorf("bundle does not serialize: %w", err)
	} else if len(canonical) > MaxBundleBytes {
		return fmt.Errorf("bundle serializes to %d bytes; the limit is %d", len(canonical), MaxBundleBytes)
	}
	return nil
}

// CanonicalBundleJSON returns the canonical serialization of b — Go's
// encoding/json, which emits struct fields in declaration order and map keys
// sorted, so two semantically equal bundles always serialize byte-identically.
// This is the form stored in skill_versions.bundle and the input to the
// content digest.
func CanonicalBundleJSON(b *SkillBundle) ([]byte, error) {
	if err := ValidateBundle(b); err != nil {
		return nil, err
	}
	return json.Marshal(b)
}

// ensureJSONEOF verifies that a decoder that has read one JSON value has
// nothing but WELL-FORMED whitespace left: the next token must be io.EOF
// exactly. A nil token means a second value followed; any other error is
// malformed trailing text — neither reads as success. Retained for callers
// that drive their own json.Decoder and want the same exactly-one-value rule
// as decodeStrictOne.
func ensureJSONEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON content")
	}
	return nil
}

// VersionDigest computes the content digest of a skill version:
//
//	"sha256:" + hex(sha256(bundleJSON || 0x00 || contractJSON))
//
// over the CANONICAL serializations of the bundle and contract. The digest
// identifies version CONTENT: it does not include the skill id or the version
// number, so the same content published to two skills (or re-imported in
// Batch 4A) yields the same digest, which is what makes seed/import
// content-verified and idempotent.
//
// bundleJSON and contractJSON are passed in already-canonicalized rather than
// re-marshaled here, so the digest is computed over exactly the bytes that are
// about to be stored.
func VersionDigest(bundleJSON, contractJSON []byte) string {
	h := sha256.New()
	h.Write(bundleJSON)
	h.Write([]byte{0})
	h.Write(contractJSON)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
