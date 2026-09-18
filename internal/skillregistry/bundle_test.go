package skillregistry

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// validBundle is the reference closed bundle every happy-path test starts from.
func validBundle() SkillBundle {
	return SkillBundle{
		Entry: "SKILL.md",
		Files: []SkillFile{
			{Path: "SKILL.md", Content: "# Grill me\n"},
		},
		License: BundleLicense{Name: "MIT"},
	}
}

func mustBundleJSON(t *testing.T, b SkillBundle) []byte {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	return raw
}

func TestDecodeBundleAcceptsAValidClosedBundle(t *testing.T) {
	b := validBundle()
	b.Provenance = BundleProvenance{
		Source:          "polyforge/skills/grill-me",
		UpstreamURL:     "https://example.com/grill-me",
		UpstreamCommit:  "abc123",
		UpstreamLicense: "MIT",
		Notes:           "imported for aihub#708",
	}
	got, err := DecodeBundle(mustBundleJSON(t, b))
	if err != nil {
		t.Fatalf("DecodeBundle: %v", err)
	}
	if got.Entry != "SKILL.md" || len(got.Files) != 1 || got.License.Name != "MIT" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.Provenance.UpstreamURL != b.Provenance.UpstreamURL {
		t.Errorf("provenance lost: %+v", got.Provenance)
	}
}

func TestDecodeBundleRejectsUnknownKeysEverywhere(t *testing.T) {
	// The closed-bundle rule: no unknown key at ANY level. This is the
	// "no dynamic upstream fetch / no symlinks / no runtime imports" defence:
	// there is no field to carry such an instruction, and an attempted one is
	// an error naming the key.
	for name, raw := range map[string]string{
		"top level":             `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT"},"fetch_url":"https://evil"}`,
		"file level url":        `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x","url":"https://evil"}],"license":{"name":"MIT"}}`,
		"file level symlink":    `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x","symlink_to":"/etc/passwd"}],"license":{"name":"MIT"}}`,
		"license level":         `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT","extra":1}}`,
		"license wrong case":    `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"Name":"MIT"}}`,
		"license null entry":    `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT","notice":null}}`,
		"provenance level":      `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT"},"provenance":{"import_ref":"main"}}`,
		"provenance wrong case": `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT"},"provenance":{"Source":"upstream"}}`,
		"provenance null entry": `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT"},"provenance":{"notes":null}}`,
	} {
		if _, err := DecodeBundle([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeBundle accepted a bundle with an unknown key; want a named refusal", name)
		} else if !strings.Contains(err.Error(), "supported keys") && !strings.Contains(err.Error(), "does not support") && !strings.Contains(err.Error(), "must not be null") {
			t.Errorf("%s: refusal should name the closed vocabulary; got %v", name, err)
		}
	}
}

func TestDecodeBundleRejectsTrailingContent(t *testing.T) {
	raw := append(mustBundleJSON(t, validBundle()), []byte(`{"entry":"x"}`)...)
	if _, err := DecodeBundle(raw); err == nil {
		t.Error("two JSON objects pasted together were accepted as one bundle")
	}
}

func TestValidateBundleEnforcesTheClosedSetRules(t *testing.T) {
	for name, mut := range map[string]func(*SkillBundle){
		"entry empty":              func(b *SkillBundle) { b.Entry = "" },
		"entry traversal":          func(b *SkillBundle) { b.Entry = "../SKILL.md" },
		"entry absolute":           func(b *SkillBundle) { b.Entry = "/SKILL.md" },
		"entry outside the bundle": func(b *SkillBundle) { b.Entry = "OTHER.md" },
		"no files":                 func(b *SkillBundle) { b.Files = nil },
		"file path traversal":      func(b *SkillBundle) { b.Files[0].Path = "a/../SKILL.md" },
		"file path absolute":       func(b *SkillBundle) { b.Files[0].Path = "/SKILL.md" },
		"file path empty":          func(b *SkillBundle) { b.Files[0].Path = "" },
		"duplicate file paths": func(b *SkillBundle) {
			b.Files = append(b.Files, SkillFile{Path: "SKILL.md", Content: "second"})
		},
		"unknown encoding": func(b *SkillBundle) {
			b.Files[0].Encoding = "rot13"
		},
		"invalid base64": func(b *SkillBundle) {
			b.Files[0].Encoding = EncodingFileBase64
			b.Files[0].Content = "not base64!!!"
		},
		"license missing": func(b *SkillBundle) { b.License = BundleLicense{} },
		"too many files": func(b *SkillBundle) {
			b.Files = make([]SkillFile, MaxBundleFiles+1)
			for i := range b.Files {
				b.Files[i] = SkillFile{Path: "f", Content: "x"}
			}
			// paths are duplicated too, but the count check fires first
		},
		"file too large": func(b *SkillBundle) {
			b.Files[0].Content = strings.Repeat("x", MaxFileBytes+1)
		},
	} {
		b := validBundle()
		mut(&b)
		if err := ValidateBundle(&b); err == nil {
			t.Errorf("%s: ValidateBundle accepted an invalid bundle", name)
		}
	}
}

func TestValidateBundleAcceptsBase64AndItsSizeLimit(t *testing.T) {
	b := validBundle()
	b.Files = append(b.Files, SkillFile{
		Path:     "icon.bin",
		Content:  base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 0xff}),
		Encoding: EncodingFileBase64,
	})
	if err := ValidateBundle(&b); err != nil {
		t.Fatalf("valid base64 file rejected: %v", err)
	}
}

func TestCanonicalBundleJSONIsDeterministic(t *testing.T) {
	// Two semantically equal bundles must serialize byte-identically: the
	// stored form and the digest both depend on it.
	b1 := validBundle()
	b2 := validBundle() // same content, different struct identity
	j1, err := CanonicalBundleJSON(&b1)
	if err != nil {
		t.Fatalf("canonicalize b1: %v", err)
	}
	j2, err := CanonicalBundleJSON(&b2)
	if err != nil {
		t.Fatalf("canonicalize b2: %v", err)
	}
	if string(j1) != string(j2) {
		t.Errorf("canonical form is not deterministic:\n%s\n%s", j1, j2)
	}
}

func TestVersionDigestIdentifiesContentNotIdentity(t *testing.T) {
	b := validBundle()
	c := SkillContract{Capabilities: []Capability{CapAuthoring}, Runtime: RuntimeSpec{}}
	bJSON, _ := CanonicalBundleJSON(&b)
	cJSON, _ := CanonicalContractJSON(&c)

	d1 := VersionDigest(bJSON, cJSON)
	if !strings.HasPrefix(d1, "sha256:") || len(d1) != len("sha256:")+64 {
		t.Fatalf("digest %q is not sha256:<64 hex>", d1)
	}
	// Same content, same digest — regardless of which skill or version it
	// would be stored under (there is no skill id in the input).
	if d2 := VersionDigest(bJSON, cJSON); d2 != d1 {
		t.Errorf("same content produced two digests: %q vs %q", d1, d2)
	}
	// Different content, different digest.
	other, _ := CanonicalBundleJSON(&SkillBundle{
		Entry: "SKILL.md", Files: []SkillFile{{Path: "SKILL.md", Content: "different"}},
		License: BundleLicense{Name: "MIT"},
	})
	if d3 := VersionDigest(other, cJSON); d3 == d1 {
		t.Error("different bundles produced the same digest")
	}
	// Same bundle, different contract: different digest.
	c2 := c
	c2.Runtime.Interactive = true
	c2JSON, _ := CanonicalContractJSON(&c2)
	if d4 := VersionDigest(bJSON, c2JSON); d4 == d1 {
		t.Error("a contract change did not change the digest")
	}
}

// ─── aihub#708 Batch 1A repair: ancestor/file collisions ────────────────────

// TestValidateBundleRejectsAncestorFileCollisions: "a" and "a/SKILL.md"
// cannot coexist — a filesystem cannot hold a file and a directory at one
// name, so a bundle carrying both is unmaterializable. The refusal must be
// symmetric: whichever of the two is listed first, the collision is the same
// bundle.
func TestValidateBundleRejectsAncestorFileCollisions(t *testing.T) {
	collide := func(order int) SkillBundle {
		files := []SkillFile{
			{Path: "a", Content: "file at a"},
			{Path: "a/SKILL.md", Content: "file under a/"},
		}
		if order == 1 {
			files[0], files[1] = files[1], files[0]
		}
		return SkillBundle{Entry: files[0].Path, Files: files, License: BundleLicense{Name: "MIT"}}
	}
	for _, order := range []int{0, 1} {
		b := collide(order)
		err := ValidateBundle(&b)
		if err == nil {
			t.Errorf("order %d: ValidateBundle accepted a file/ancestor-directory collision (%q vs %q)", order, b.Files[0].Path, b.Files[1].Path)
			continue
		}
		if !strings.Contains(err.Error(), "collides") || !strings.Contains(err.Error(), "parent directory") {
			t.Errorf("order %d: refusal should name the collision; got %v", order, err)
		}
	}
	// Deeper nesting: "a/b" (file) vs "a/b/c.md" (needs a/b as a directory).
	deep := SkillBundle{
		Entry:   "a/b/c.md",
		Files:   []SkillFile{{Path: "a/b", Content: "x"}, {Path: "a/b/c.md", Content: "y"}},
		License: BundleLicense{Name: "MIT"},
	}
	if err := ValidateBundle(&deep); err == nil {
		t.Error("nested file/ancestor collision was accepted")
	}
	// Files that merely share a prefix are fine: "a" and "ab.md" have no
	// directory relationship, and "a/SKILL.md" plus "a/b.md" are siblings.
	ok := SkillBundle{
		Entry: "a/SKILL.md",
		Files: []SkillFile{
			{Path: "a/SKILL.md", Content: "entry"},
			{Path: "a/b.md", Content: "sibling under a/"},
			{Path: "ab.md", Content: "prefix-adjacent, not an ancestor"},
		},
		License: BundleLicense{Name: "MIT"},
	}
	if err := ValidateBundle(&ok); err != nil {
		t.Errorf("a bundle without collisions was refused: %v", err)
	}
	// DecodeBundle takes the same road (closed decode then validate).
	if _, err := DecodeBundle([]byte(`{"entry":"a","files":[{"path":"a","content":"x"},{"path":"a/SKILL.md","content":"y"}],"license":{"name":"MIT"}}`)); err == nil {
		t.Error("DecodeBundle accepted a file/ancestor collision")
	}
}

// TestDecodeBundleRejectsMalformedTrailingText: trailing content is refused
// whether it is a second document or malformed bytes — a trailing syntax
// error used to read as success.
func TestDecodeBundleRejectsMalformedTrailingText(t *testing.T) {
	base := `{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT"}}`
	for name, raw := range map[string]string{
		"garbage":       base + " garbage",
		"second object": base + `{"entry":"y"}`,
		"stray bracket": base + "]",
		"nul byte":      base + "\x00",
	} {
		if _, err := DecodeBundle([]byte(raw)); err == nil {
			t.Errorf("%s: DecodeBundle accepted trailing text after the bundle", name)
		}
	}
	// Duplicate keys anywhere in a bundle are ambiguous input, not a bundle.
	dup := `{"entry":"SKILL.md","entry":"OTHER.md","files":[{"path":"SKILL.md","content":"x"}],"license":{"name":"MIT"}}`
	if _, err := DecodeBundle([]byte(dup)); err == nil {
		t.Error("DecodeBundle accepted duplicate keys; which entry wins would depend on the decoder")
	}
}
