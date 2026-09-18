package controller

// materialize.go — pinned-bundle materialization for the DB workflow path
// (aihub#708 Batch 2B).
//
// A pinned skill version's bundle is a CLOSED file set (internal/skillregistry:
// decoding refuses unknown keys, so no bundle can carry a "fetch me at
// runtime" instruction, a symlink target or an out-of-band reference). Turning
// it into files on disk is therefore pure data movement — but it is still
// untrusted stored content reaching a filesystem, so the write is defended in
// depth:
//
//   - every file path is re-validated with skillregistry.ValidateBundlePath
//     (the same normalized-relative-path rule DecodeBundle enforced; restated
//     here so a future caller that builds a bundle in Go cannot skip it);
//   - the write target is re-derived from a cleaned join and checked to stay
//     INSIDE the materialization directory (traversal-safe by construction,
//     not by trust in the stored path);
//   - the directory is created 0700 and files are written 0600 — a materialized
//     bundle is prompt scaffolding for one invocation, private to this run.
//
// The entry file's decoded content is returned as the prompt; the OTHER files
// are supporting material the entry may reference by relative path, which is
// the whole reason materialization exists: a single-file bundle needs none of
// this, and a bundle with includes is exactly the case the previous Batch 2
// implementation refused ("pinned bundle has multiple files; controller cannot
// execute includes without materialization").

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// MaterializeBundle writes every file of bundle under dir and returns the
// decoded content of the bundle's entry file.
//
// dir must not already exist as a non-directory; it is created with 0700 (and
// any missing parents), because the whole point of the private temp dir is
// that nobody but this invocation's worker reads it.
func MaterializeBundle(bundle *skillregistry.SkillBundle, dir string) (string, error) {
	if bundle == nil {
		return "", errors.New("materialize: bundle is nil")
	}
	if dir == "" {
		return "", errors.New("materialize: directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("materialize: create %s: %w", dir, err)
	}

	var entry string
	for i := range bundle.Files {
		f := &bundle.Files[i]
		if err := skillregistry.ValidateBundlePath(f.Path); err != nil {
			return "", fmt.Errorf("materialize: refuse bundled path: %w", err)
		}
		target, err := safeBundleTarget(dir, f.Path)
		if err != nil {
			return "", err
		}
		var data []byte
		switch f.Encoding {
		case "", skillregistry.EncodingFileUTF8:
			data = []byte(f.Content)
		case skillregistry.EncodingFileBase64:
			decoded, derr := base64.StdEncoding.DecodeString(f.Content)
			if derr != nil {
				return "", fmt.Errorf("materialize: bundle file %q: base64 content does not decode: %w", f.Path, derr)
			}
			data = decoded
		default:
			return "", fmt.Errorf("materialize: bundle file %q has unknown encoding %q", f.Path, f.Encoding)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", fmt.Errorf("materialize: create parent of %q: %w", f.Path, err)
		}
		// O_EXCL semantics are not needed: the directory was created by this
		// call and bundle paths are unique (ValidateBundle), so a collision
		// would mean somebody else is writing into a 0700 directory named
		// after a server-minted step_attempt_id.
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return "", fmt.Errorf("materialize: write bundle file %q: %w", f.Path, err)
		}
		if f.Path == bundle.Entry {
			if f.Encoding == skillregistry.EncodingFileBase64 {
				return "", fmt.Errorf("materialize: bundle entry %q is binary (base64) and cannot be executed as a prompt", f.Path)
			}
			entry = f.Content
		}
	}
	if strings.TrimSpace(entry) == "" {
		return "", errors.New("materialize: pinned bundle entry is empty")
	}
	return entry, nil
}

// safeBundleTarget joins a bundle path under dir and refuses anything that
// would escape dir. ValidateBundlePath has already rejected absolute paths,
// "~", backslashes, drive prefixes, empty/dot-only segments and control
// characters; this second check is the filesystem-side belt-and-braces that
// does not trust the earlier validation at all — a traversal that somehow
// survived the registry's rules would have to survive a cleaned-join plus
// Rel()-based containment check here too.
func safeBundleTarget(dir, path string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(path))
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return "", fmt.Errorf("materialize: bundle path %q is not placeable under %s: %w", path, dir, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("materialize: bundle path %q escapes the materialization directory", path)
	}
	return target, nil
}
