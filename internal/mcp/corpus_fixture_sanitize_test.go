package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The golden corpus fixtures under testdata/corpus/ are captured from real
// transcripts of real polyforge sessions (aihub#412). Sanitization of that
// capture is a GATE, not a step: the extractor scrubs secrets on the way out,
// and this file re-scans what actually landed in the tree. The two must be
// changed together — scripts/pf_corpus_extract.py chooses its replacement
// strings specifically so that its own output cannot match the patterns below.
//
// Why re-scan at all, when the extractor already scrubbed? Because the
// extractor's rules are keyed on field names and value shapes it knows about,
// and a fixture can also be hand-edited or hand-added later. The gate has to
// read the committed bytes, not trust the producer.

// secretPattern is one shape that must never appear in a committed fixture.
type secretPattern struct {
	name string
	re   *regexp.Regexp
}

// The required set from aihub#412, plus the machine-identity rules the
// extractor promises in testdata/corpus/README.md.
//
// Note the api_key/session_secret patterns require a NON-EMPTY value. That is
// deliberate and it is why the extractor empties those fields rather than
// substituting a placeholder: `"api_key": "<redacted>"` would still match
// `"[^"]+"`, and the cheapest way to make that green would have been to
// allowlist the placeholder here — an escape hatch in the gate's own file.
// `"api_key": ""` cannot match, so compliance is cheaper than evasion.
var corpusSecretPatterns = []secretPattern{
	{"hex64", regexp.MustCompile(`(^|[^0-9a-fA-F])[0-9a-fA-F]{64}([^0-9a-fA-F]|$)`)},
	{"sk-token", regexp.MustCompile(`sk-[A-Za-z0-9]{10,}`)},
	{"bearer", regexp.MustCompile(`(?i)bearer\s+\S+`)},
	{"api_key-field", regexp.MustCompile(`"api_key"\s*:\s*"[^"]+"`)},
	{"session_secret-field", regexp.MustCompile(`"session_secret"\s*:\s*"[^"]+"`)},
	{"absolute-home-path", regexp.MustCompile(`/root[/"]|/home/[A-Za-z0-9]|/Users/[A-Za-z0-9]`)},
}

type secretHit struct {
	file    string
	pattern string
	sample  string
}

type corpusScan struct {
	files int
	tools []string
	hits  []secretHit
	bad   []string // structural problems, kept separate from secret hits
}

// scanCorpusTree walks a fixture tree exactly the way the gate does. It is a
// named function so the negative control below can drive the SAME code path
// over a tree with a planted secret: a scanner that is only ever pointed at
// the clean tree proves nothing about whether it can see a dirty one.
func scanCorpusTree(root string) (corpusScan, error) {
	var s corpusScan
	toolSet := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		s.files++
		toolSet[filepath.Base(filepath.Dir(path))] = true

		// Structural checks. A fixture that is not parseable JSON, or that has
		// lost its request/response, is a broken fixture even if it holds no
		// secret — and an unparseable file is also a file whose contents the
		// producer never really controlled.
		var fx struct {
			Tool     string          `json:"tool"`
			Role     string          `json:"role"`
			Request  json.RawMessage `json:"request"`
			Response json.RawMessage `json:"response"`
		}
		if jsonErr := json.Unmarshal(data, &fx); jsonErr != nil {
			s.bad = append(s.bad, fmt.Sprintf("%s: not valid JSON: %v", rel, jsonErr))
		} else {
			if fx.Tool == "" || fx.Request == nil || fx.Response == nil {
				s.bad = append(s.bad, fmt.Sprintf("%s: missing tool/request/response", rel))
			}
			if dir := filepath.Base(filepath.Dir(path)); fx.Tool != dir {
				s.bad = append(s.bad, fmt.Sprintf("%s: tool %q does not match directory %q", rel, fx.Tool, dir))
			}
			switch fx.Role {
			case "happy", "error", "edge":
			default:
				s.bad = append(s.bad, fmt.Sprintf("%s: unexpected role %q", rel, fx.Role))
			}
		}

		for _, p := range corpusSecretPatterns {
			if m := p.re.FindIndex(data); m != nil {
				lo := m[0]
				hi := m[1]
				if hi-lo > 96 {
					hi = lo + 96
				}
				s.hits = append(s.hits, secretHit{
					file:    rel,
					pattern: p.name,
					sample:  strings.TrimSpace(string(data[lo:hi])),
				})
			}
		}
		return nil
	})
	for t := range toolSet {
		s.tools = append(s.tools, t)
	}
	sort.Strings(s.tools)
	return s, err
}

const (
	// Floors, so the gate cannot pass by scanning nothing. A tree that lost
	// its fixtures — deleted, moved, or regenerated from a thinner corpus —
	// is a regression, and without these the scan would happily report
	// "0 secrets found" over 0 files and exit 0.
	minCorpusFixtureFiles = 100
	minCorpusFixtureTools = 42
)

func TestCorpusFixturesAreSanitized(t *testing.T) {
	root := filepath.Join("testdata", "corpus")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture tree %s is unreadable: %v", root, err)
	}
	s, err := scanCorpusTree(root)
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if s.files < minCorpusFixtureFiles {
		t.Errorf("scanned only %d fixture files, want >= %d: the gate must not pass by scanning an empty tree",
			s.files, minCorpusFixtureFiles)
	}
	if len(s.tools) < minCorpusFixtureTools {
		t.Errorf("fixtures cover only %d tools, want >= %d (have: %v)",
			len(s.tools), minCorpusFixtureTools, s.tools)
	}
	for _, b := range s.bad {
		t.Errorf("malformed fixture: %s", b)
	}
	for _, h := range s.hits {
		t.Errorf("SECRET LEAK in fixture %s: pattern %q matched %q\n"+
			"Fixtures are captured from real sessions. Do not hand-edit this file to pass; "+
			"fix the sanitizer in scripts/pf_corpus_extract.py and regenerate the tree.",
			h.file, h.pattern, h.sample)
	}
	t.Logf("scanned %d fixture files across %d tools, no secret shapes found", s.files, len(s.tools))
}

// TestCorpusSecretScanDetectsPlantedSecrets is the negative control. It plants
// one secret of every shape into a throwaway tree and drives the same
// scanCorpusTree the gate above uses. Without it, "no hits on the clean tree"
// is equally consistent with a scanner that can no longer match anything —
// a regexp typo, a walk that never descends, an early return.
func TestCorpusSecretScanDetectsPlantedSecrets(t *testing.T) {
	planted := []struct {
		pattern string
		body    string
	}{
		{"hex64", `{"tool":"pf_x","role":"happy","request":{},"response":{"session":"9f2c1a4b7e8d0356af91bc2d4e6f80137a5b9c8d2e1f0a3b4c5d6e7f8091a2b3"}}`},
		{"sk-token", `{"tool":"pf_x","role":"happy","request":{},"response":{"k":"sk-ABCdef0123456789xyz"}}`},
		{"bearer", `{"tool":"pf_x","role":"happy","request":{},"response":{"h":"Bearer eyJhbGciOiJIUzI1NiJ9"}}`},
		{"api_key-field", `{"tool":"pf_x","role":"happy","request":{},"response":{"api_key": "pfk_live_not_empty"}}`},
		{"session_secret-field", `{"tool":"pf_x","role":"happy","request":{},"response":{"session_secret": "deadbeef"}}`},
		{"absolute-home-path", `{"tool":"pf_x","role":"happy","request":{},"response":{"p":"/root/.polyforge/state/wi_1.json"}}`},
	}
	for _, p := range planted {
		t.Run(p.pattern, func(t *testing.T) {
			dir := t.TempDir()
			toolDir := filepath.Join(dir, "pf_x")
			if err := os.MkdirAll(toolDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(toolDir, "happy.json"), []byte(p.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			s, err := scanCorpusTree(dir)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if s.files != 1 {
				t.Fatalf("scanned %d files, want 1 — the walk did not reach the planted fixture", s.files)
			}
			var got []string
			for _, h := range s.hits {
				got = append(got, h.pattern)
			}
			found := false
			for _, g := range got {
				if g == p.pattern {
					found = true
				}
			}
			if !found {
				t.Errorf("planted a %s secret and the scanner did not flag it (hits: %v). "+
					"The gate is blind to this shape.", p.pattern, got)
			}
		})
	}
}

// TestCorpusSecretScanFloorsRejectEmptyTree pins the other way the gate could
// go quiet: an empty tree must not look clean. The floors are asserted by the
// gate itself, so this checks the scan reports the emptiness it would judge.
func TestCorpusSecretScanFloorsRejectEmptyTree(t *testing.T) {
	dir := t.TempDir()
	s, err := scanCorpusTree(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if s.files != 0 || len(s.hits) != 0 {
		t.Fatalf("empty tree scanned as files=%d hits=%d", s.files, len(s.hits))
	}
	if s.files >= minCorpusFixtureFiles || len(s.tools) >= minCorpusFixtureTools {
		t.Fatal("floors would accept an empty tree")
	}
}
