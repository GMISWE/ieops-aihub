package skillregistry

// import.go — the import half of Batch 4A (aihub#708): build a CLOSED skill
// bundle from a local directory tree, the inverse of materialization, so a
// body that already exists as files (a plugin skill folder, a crystallized
// workflow's step bodies) can enter the registry through exactly the same
// closed shape a hand-authored bundle uses.
//
// What this deliberately is NOT:
//
//   - it is not a fetcher. There is no URL, no ref, no network: the only
//     input is a directory the caller can already read. The closed-bundle
//     rule (spec D2, "no dynamic upstream fetch") is enforced the same way
//     DecodeBundle enforces it — by there being nowhere to put such an
//     instruction.
//   - it is not a publisher. It returns a *SkillBundle; storing it is
//     CreateSkill/PublishSkillVersion in internal/domain, under the
//     caller's own authorization, private by default.
//   - it does not stamp the clock. ImportedAt is the CALLER's to set. An
//     import that stamped its own timestamp could never be idempotent:
//     re-importing an unchanged tree would produce different bytes, a
//     different digest, and therefore a duplicate version. Leaving time
//     out is what makes "import again" content-addressed and safely a
//     no-op (the same rule PlanSeed applies to the seed set).
//
// Determinism: the file list is sorted by path, contents are read verbatim
// (UTF-8 text in, base64 for non-UTF-8 bytes), and nothing else varies. Two
// imports of the same tree produce byte-identical bundles, therefore the
// same VersionDigest, therefore the same idempotency answer.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// ImportOptions configures BuildImportedBundle. License is REQUIRED and its
// Name must be non-empty: an unlabelled bundle is a redistribution hazard
// the registry cannot audit later (the same rule every stored version
// already follows). Provenance records where the tree came from; for MIT
// content set UpstreamLicense to "MIT" and carry the upstream notice in
// License.Notice (D2: preserve it).
type ImportOptions struct {
	Entry      string
	License    BundleLicense
	Provenance BundleProvenance
}

// BuildImportedBundle walks root and returns the closed bundle containing
// every regular file under it, deterministically ordered.
//
// Refusals, all named:
//   - root is not a directory, or cannot be walked;
//   - Entry is empty or not one of the imported files (execution can never
//     start outside the bundle);
//   - any file path fails ValidateBundlePath (absolute, traversal, control
//     characters, backslashes, drive prefixes, empty segments...);
//   - any file is a symlink, a socket/device, or otherwise not a regular
//     file — a symlink in a bundle is unmaterializable by rule (D2), so it
//     is refused at import rather than at extraction;
//   - any path has a ".git" segment: repository metadata is not skill
//     content, and importing it would leak local state into a shareable
//     bundle;
//   - any file exceeds MaxFileBytes;
//   - the resulting bundle fails ValidateBundle (duplicate paths, size
//     caps, license missing).
func BuildImportedBundle(root string, opts ImportOptions) (*SkillBundle, error) {
	if opts.License.Name == "" {
		return nil, fmt.Errorf("import: license.name is required; an unlabelled bundle cannot be stored")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("import: read %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("import: %q is not a directory", root)
	}
	if opts.Entry == "" {
		return nil, fmt.Errorf("import: entry is required; it must name one of the imported files")
	}

	var files []SkillFile
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// Repository metadata is never skill content (see the doc comment).
		for _, seg := range strings.Split(rel, "/") {
			if seg == ".git" {
				return fmt.Errorf("import: refusing to bundle repository metadata %q", rel)
			}
		}
		// ValidateBundlePath is the single authority on legal paths; running
		// it per file means the error names the file, not the bundle.
		if err := ValidateBundlePath(rel); err != nil {
			return fmt.Errorf("import: %w", err)
		}
		if seen[rel] {
			// Unreachable in practice (WalkDir visits each path once), but
			// the duplicate check is ValidateBundle's job and this keeps the
			// error message local.
			return fmt.Errorf("import: path %q appears twice", rel)
		}
		// Symlinks and other non-regular files: a symlink cannot be stored
		// as a closed bundle's content without resolving it, and resolving
		// it silently would smuggle a target outside root into the bundle.
		if !d.Type().IsRegular() {
			return fmt.Errorf("import: %q is not a regular file (symlinks and special files are refused; a bundle is a closed file set, never a directory tree with indirection)", rel)
		}
		content, readErr := os.ReadFile(p)
		if readErr != nil {
			return fmt.Errorf("import: read %q: %w", rel, readErr)
		}
		if len(content) > MaxFileBytes {
			return fmt.Errorf("import: file %q is %d bytes; the limit is %d", rel, len(content), MaxFileBytes)
		}
		f := SkillFile{Path: rel, Content: string(content), Encoding: EncodingFileUTF8}
		if !utf8.Valid(content) {
			f.Encoding = EncodingFileBase64
			f.Content = base64.StdEncoding.EncodeToString(content)
		}
		seen[rel] = true
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("import: %q contains no files", root)
	}

	// Deterministic order, independent of the filesystem's iteration order.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	entry := filepath.ToSlash(opts.Entry)
	found := false
	for _, f := range files {
		if f.Path == entry {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("import: entry %q is not one of the imported files", entry)
	}

	bundle := &SkillBundle{
		Entry:      entry,
		Files:      files,
		Provenance: opts.Provenance,
		License:    opts.License,
	}
	if err := ValidateBundle(bundle); err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	return bundle, nil
}

// ContentDigest returns the content digest of a bundle+contract pair:
// canonical bytes, then VersionDigest. It is the one comparison an
// idempotent seed or import runs — import the tree again, digest the result,
// and a digest equal to a stored version's means the content is already
// published (publish nothing); a different digest means a new version.
func ContentDigest(bundle *SkillBundle, contract *SkillContract) (string, error) {
	bundleJSON, err := CanonicalBundleJSON(bundle)
	if err != nil {
		return "", fmt.Errorf("content digest: bundle: %w", err)
	}
	contractJSON, err := CanonicalContractJSON(contract)
	if err != nil {
		return "", fmt.Errorf("content digest: contract: %w", err)
	}
	return VersionDigest(bundleJSON, contractJSON), nil
}

// ─── Applying an import: the callable import operation ──────────────────────
//
// The local, in-process callable entry for the import half, symmetric with
// ApplySeed in seed.go (aihub#708 Batch 4B review). Same rules, same port,
// same gaps: the server/CLI wiring above this package is still another
// batch's work — reported, not faked.

// ApplyImport imports the directory tree at root as (name)'s next version
// through the same authenticated SeedStore port ApplySeed uses, with the same
// content-addressed idempotency: the tree is built into a closed bundle
// (BuildImportedBundle — deterministic, no clock), digested together with the
// caller's contract (ContentDigest), and compared against the versions the
// authenticated caller already has. An identical published version means
// PRESENT — nothing is published again; different content means the next
// version under the current latest as the CAS token; no identity means the
// identity is created first. Every publish is private by default and carries
// the exact CAS token; a nil store is refused — there is no anonymous import.
//
// contract is REQUIRED: a version without a runtime contract cannot be
// pinned into a flow, and inventing one here (capabilities the author never
// chose) would be a silent lie — the caller supplies the contract it wants
// the imported body to run under, and it is validated before anything is
// published. name must be registry-legal (the domain's rule, mirrored here
// so an illegal name fails before the store is touched).
func ApplyImport(ctx context.Context, store SeedStore, name string, root string, opts ImportOptions, contract *SkillContract) (SeedApplyResult, error) {
	if store == nil {
		return SeedApplyResult{}, errors.New("ApplyImport: no seed store: an anonymous import operation does not exist")
	}
	if !seedNameRE.MatchString(name) {
		return SeedApplyResult{}, fmt.Errorf("import: skill name %q does not match ^[a-z][a-z0-9-]{0,63}$", name)
	}
	if contract == nil {
		return SeedApplyResult{}, errors.New("import: a runtime contract is required; a version without one cannot be pinned into a flow")
	}
	if err := ValidateContract(contract); err != nil {
		return SeedApplyResult{}, fmt.Errorf("import: contract: %w", err)
	}
	bundle, err := BuildImportedBundle(root, opts)
	if err != nil {
		return SeedApplyResult{}, err
	}
	digest, err := ContentDigest(bundle, contract)
	if err != nil {
		return SeedApplyResult{}, err
	}
	skillID, versions, exists, err := store.SeedSkill(ctx, name)
	if err != nil {
		return SeedApplyResult{}, fmt.Errorf("import %q: read existing versions: %w", name, err)
	}
	res := SeedApplyResult{Name: name, Digest: digest}
	latest := 0
	match := 0
	for _, v := range versions {
		if v.Version < 1 {
			return SeedApplyResult{}, fmt.Errorf("import %q: existing version %d is invalid; versions start at 1", name, v.Version)
		}
		if v.Digest == "" {
			return SeedApplyResult{}, fmt.Errorf("import %q: existing version %d carries no digest; the comparison cannot be content-checked", name, v.Version)
		}
		if v.Version > latest {
			latest = v.Version
		}
		if v.Digest == digest && v.Version > match {
			match = v.Version
		}
	}
	switch {
	case match > 0:
		// Content-addressed no-op: the identical tree is already published.
		res.Op, res.SkillID, res.Version = SeedOpPresent, skillID, match
		return res, nil
	case !exists:
		id, cerr := store.CreateSeedSkill(ctx, name)
		if cerr != nil {
			return res, fmt.Errorf("import %q: create identity: %w", name, cerr)
		}
		skillID, latest = id, 0
		res.Op = SeedOpCreate
	default:
		res.Op = SeedOpPublish
	}
	res.SkillID = skillID
	bundleJSON, err := CanonicalBundleJSON(bundle)
	if err != nil {
		return res, fmt.Errorf("import %q: bundle: %w", name, err)
	}
	contractJSON, err := CanonicalContractJSON(contract)
	if err != nil {
		return res, fmt.Errorf("import %q: contract: %w", name, err)
	}
	version, perr := store.PublishSeedSkillVersion(ctx, skillID, latest, bundleJSON, contractJSON, digest)
	if perr != nil {
		return res, fmt.Errorf("import %q: publish version %d: %w", name, latest+1, perr)
	}
	if version > 0 {
		res.Version = version
	}
	return res, nil
}
