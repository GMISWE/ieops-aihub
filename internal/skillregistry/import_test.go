package skillregistry

// import_test.go — the import path's own contract (Batch 4A code, Batch 4B
// review tests): BuildImportedBundle's closed-set refusals and determinism,
// and ApplyImport's authenticated, content-addressed, idempotent publication
// through the same SeedStore port ApplySeed drives.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func importFixture(t *testing.T) (root, other string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "refs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# body\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "refs", "notes.md"), []byte("ref\n"), 0644); err != nil {
		t.Fatal(err)
	}
	other = t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "SKILL.md"), []byte("# body\nv2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return root, other
}

func importOpts() ImportOptions {
	return ImportOptions{
		Entry: "SKILL.md",
		License: BundleLicense{
			Name:   "MIT",
			URL:    "https://opensource.org/licenses/MIT",
			Notice: "MIT License\n\nCopyright (c) 2026 Someone\n\n[full notice]\n",
		},
		Provenance: BundleProvenance{Source: "test fixture"},
	}
}

func importContract() *SkillContract {
	return &SkillContract{
		Capabilities: []Capability{CapDeterministicOperation},
		Runtime:      RuntimeSpec{Interactive: false},
	}
}

func refuse(t *testing.T, name string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: got %v, want a refusal mentioning %q", name, err, want)
	}
}

func TestBuildImportedBundleIsDeterministicAndClosed(t *testing.T) {
	root, _ := importFixture(t)

	first, err := BuildImportedBundle(root, importOpts())
	if err != nil {
		t.Fatalf("BuildImportedBundle: %v", err)
	}
	if len(first.Files) != 2 || first.Files[0].Path != "SKILL.md" || first.Files[1].Path != "refs/notes.md" {
		t.Fatalf("files = %+v, want the sorted closed set", first.Files)
	}
	second, err := BuildImportedBundle(root, importOpts())
	if err != nil {
		t.Fatal(err)
	}
	d1, err := ContentDigest(first, importContract())
	if err != nil {
		t.Fatal(err)
	}
	d2, err := ContentDigest(second, importContract())
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("two imports of the same tree digested differently: %q vs %q — import is not deterministic", d1, d2)
	}

	// The refusal set, by name.
	_, err = BuildImportedBundle(root, ImportOptions{Entry: "SKILL.md"})
	refuse(t, "missing license", err, "license")
	_, err = BuildImportedBundle(root, ImportOptions{License: BundleLicense{Name: "MIT"}})
	refuse(t, "missing entry", err, "entry")
	_, err = BuildImportedBundle(root, ImportOptions{Entry: "nope.md", License: BundleLicense{Name: "MIT"}})
	refuse(t, "entry not in the tree", err, "not one of the imported files")
	_, err = BuildImportedBundle(filepath.Join(root, "SKILL.md"), importOpts())
	refuse(t, "root is a file", err, "not a directory")

	gitRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitRoot, ".git", "config"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitRoot, "SKILL.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = BuildImportedBundle(gitRoot, importOpts())
	refuse(t, ".git segment", err, "repository metadata")

	empty := t.TempDir()
	_, err = BuildImportedBundle(empty, importOpts())
	refuse(t, "empty tree", err, "no files")

	linkRoot := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "SKILL.md"), filepath.Join(linkRoot, "SKILL.md")); err != nil {
		t.Skip("symlinks unavailable on this filesystem")
	}
	_, err = BuildImportedBundle(linkRoot, importOpts())
	refuse(t, "symlink", err, "not a regular file")
}

func TestApplyImportPublishesThenIsIdempotentThenVersions(t *testing.T) {
	root, other := importFixture(t)
	store := newFakeSeedStore()

	first, err := ApplyImport(context.Background(), store, "my-skill", root, importOpts(), importContract())
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if first.Op != SeedOpCreate || first.Version != 1 || first.SkillID == "" {
		t.Fatalf("first import = %+v, want create publishing version 1", first)
	}

	// Re-importing the SAME tree is a content-addressed no-op.
	again, err := ApplyImport(context.Background(), store, "my-skill", root, importOpts(), importContract())
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again.Op != SeedOpPresent || again.Version != 1 {
		t.Fatalf("re-import = %+v, want present at version 1 (idempotency)", again)
	}

	// A CHANGED tree publishes the next version under the current latest.
	third, err := ApplyImport(context.Background(), store, "my-skill", other, importOpts(), importContract())
	if err != nil {
		t.Fatalf("changed-tree import: %v", err)
	}
	if third.Op != SeedOpPublish || third.Version != 2 {
		t.Fatalf("changed-tree import = %+v, want publish of version 2", third)
	}
	if !containsString(store.log, "publish:skill_my-skill_id@1") {
		t.Errorf("the second publish did not carry the current latest 1 as its CAS token: %v", store.log)
	}
}

func TestApplyImportRefusals(t *testing.T) {
	root, _ := importFixture(t)
	store := newFakeSeedStore()

	if _, err := ApplyImport(context.Background(), nil, "x", root, importOpts(), importContract()); err == nil {
		t.Fatal("ApplyImport(nil store) must be refused: there is no anonymous import")
	}
	if _, err := ApplyImport(context.Background(), store, "Bad_Name", root, importOpts(), importContract()); err == nil {
		t.Fatal("an illegal registry name must be refused before the store is touched")
	}
	if _, err := ApplyImport(context.Background(), store, "my-skill", root, importOpts(), nil); err == nil {
		t.Fatal("a nil contract must be refused: a version without one cannot be pinned into a flow")
	}
	bad := importContract()
	bad.Capabilities = []Capability{"not-a-capability"}
	if _, err := ApplyImport(context.Background(), store, "my-skill", root, importOpts(), bad); err == nil {
		t.Fatal("a contract outside the closed capability vocabulary must be refused")
	}
	if _, err := ApplyImport(context.Background(), store, "my-skill", filepath.Join(root, "missing"), importOpts(), importContract()); err == nil {
		t.Fatal("a missing root must be refused")
	}
}

func TestApplyImportExistingIdentityNoVersionsPublishesVersionOne(t *testing.T) {
	root, _ := importFixture(t)
	store := newFakeSeedStore()
	// An identity that exists but has never published: publish v1, never a create.
	store.skills["my-skill"] = &fakeSeed{id: "skill_my-skill_id"}
	res, err := ApplyImport(context.Background(), store, "my-skill", root, importOpts(), importContract())
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if res.Op != SeedOpPublish || res.Version != 1 {
		t.Fatalf("res = %+v, want publish of version 1 against the empty identity", res)
	}
	for _, entry := range store.log {
		if entry == "create:my-skill" {
			t.Errorf("the operation attempted a create for an existing identity: %v", store.log)
		}
	}
}
