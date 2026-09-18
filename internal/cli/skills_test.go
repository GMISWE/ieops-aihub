package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

type cliSeedRecord struct {
	id       string
	versions []skillregistry.ExistingVersion
}

type cliSeedStore struct {
	rows map[string]*cliSeedRecord
	byID map[string]*cliSeedRecord
}

func newCLISeedStore() *cliSeedStore {
	return &cliSeedStore{rows: map[string]*cliSeedRecord{}, byID: map[string]*cliSeedRecord{}}
}

func (s *cliSeedStore) SeedSkill(_ context.Context, name string) (string, []skillregistry.ExistingVersion, bool, error) {
	r, ok := s.rows[name]
	if !ok {
		return "", nil, false, nil
	}
	return r.id, append([]skillregistry.ExistingVersion(nil), r.versions...), true, nil
}

func (s *cliSeedStore) CreateSeedSkill(_ context.Context, name string) (string, error) {
	if _, exists := s.rows[name]; exists {
		return "", fmt.Errorf("duplicate skill %s", name)
	}
	r := &cliSeedRecord{id: "skill_" + name}
	s.rows[name], s.byID[r.id] = r, r
	return r.id, nil
}

func (s *cliSeedStore) PublishSeedSkillVersion(_ context.Context, id string, expected int, bundle, contract []byte, digest string) (int, error) {
	r := s.byID[id]
	if r == nil || len(r.versions) != expected {
		return 0, fmt.Errorf("expected_latest mismatch")
	}
	b, err := skillregistry.DecodeBundle(bundle)
	if err != nil {
		return 0, err
	}
	c, err := skillregistry.DecodeContract(contract)
	if err != nil {
		return 0, err
	}
	computed, err := skillregistry.ContentDigest(b, c)
	if err != nil || computed != digest {
		return 0, fmt.Errorf("digest mismatch: %v", err)
	}
	version := expected + 1
	r.versions = append(r.versions, skillregistry.ExistingVersion{Version: version, Digest: digest})
	return version, nil
}

func TestRunSkillsSeedAndImportAreIdempotent(t *testing.T) {
	store := newCLISeedStore()
	first, err := runSkills(t.Context(), store, []string{"seed"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(first.([]skillregistry.SeedApplyResult)) != 8 {
		t.Fatalf("seed result = %#v", first)
	}
	second, err := runSkills(t.Context(), store, []string{"seed"})
	if err != nil {
		t.Fatalf("seed rerun: %v", err)
	}
	for _, result := range second.([]skillregistry.SeedApplyResult) {
		if result.Op != skillregistry.SeedOpPresent {
			t.Fatalf("seed rerun published content: %+v", result)
		}
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "contract.json")
	contract, _ := json.Marshal(skillregistry.SkillContract{
		Capabilities: []skillregistry.Capability{skillregistry.CapAuthoring},
		Runtime:      skillregistry.RuntimeSpec{Interactive: false},
	})
	if err := os.WriteFile(contractPath, contract, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"import", "--name=fixture", "--root=" + root, "--entry=SKILL.md", "--contract-file=" + contractPath, "--license-name=Proprietary"}
	got, err := runSkills(t.Context(), store, args)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got.(skillregistry.SeedApplyResult).Op != skillregistry.SeedOpCreate {
		t.Fatalf("first import = %+v", got)
	}
	again, err := runSkills(t.Context(), store, args)
	if err != nil {
		t.Fatalf("import rerun: %v", err)
	}
	if again.(skillregistry.SeedApplyResult).Op != skillregistry.SeedOpPresent {
		t.Fatalf("unchanged import rerun = %+v", again)
	}
}

func TestRunSkillsImportRefusesImplicitContractOrLicense(t *testing.T) {
	store := newCLISeedStore()
	if _, err := runSkills(t.Context(), store, []string{"import", "--name=x"}); err == nil {
		t.Fatal("import accepted missing root/entry/contract/license")
	}
	if _, err := runSkills(t.Context(), store, []string{"seed", "--public"}); err == nil {
		t.Fatal("seed accepted a visibility-widening flag")
	}
}
