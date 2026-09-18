package modelruntime

import (
	"errors"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

func okLookPath(string) (string, error) { return "/usr/bin/fake", nil }

func failLookPath(string) (string, error) { return "", errors.New("not on PATH") }

// TestPreflightHappyPathPi proves the full gate end-to-end on one harness:
// binary resolved, exact catalog entry, uses authorized, effort mapped from
// the fixture catalog, and the command carrying it.
func TestPreflightHappyPathPi(t *testing.T) {
	c := testCatalog(t)
	ready, refusal := Preflight(c, PreflightRequest{
		Candidate: workflow.ModelCandidate{Harness: "pi", Model: "sub2api-glm/glm-5.3", Effort: "high"},
		Grant:     workflow.StepGrant{Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared},
		Requested: []skillregistry.Capability{skillregistry.CapAuthoring},
		Prompt:    "BUILD IT",
	}, sourcesFor(t), okLookPath)
	if refusal != nil {
		t.Fatalf("unexpected refusal: %v", refusal)
	}
	if ready.Command.Path != "/usr/bin/fake" {
		t.Errorf("path = %q", ready.Command.Path)
	}
	joined := strings.Join(ready.Command.Args, " ")
	if !strings.Contains(joined, "--thinking high") || !strings.Contains(joined, "--model sub2api-glm/glm-5.3") ||
		!strings.HasSuffix(joined, "BUILD IT") {
		t.Errorf("argv = %q", joined)
	}
	if ready.Effort.Verified != EffortVerificationUnavailable {
		t.Errorf("verification must be honestly unavailable: %+v", ready.Effort)
	}
	if ready.Entry.Name != "impl-pi" {
		t.Errorf("entry = %+v", ready.Entry)
	}
}

func TestPreflightRefusals(t *testing.T) {
	c := testCatalog(t)
	codexCandidate := workflow.ModelCandidate{Harness: "codex", Model: "gpt-6-astra", Effort: "medium"}
	base := PreflightRequest{
		Candidate: codexCandidate,
		Grant:     workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired},
		Requested: []skillregistry.Capability{skillregistry.CapReview},
		Prompt:    "REVIEW",
	}

	t.Run("unknown harness spelling", func(t *testing.T) {
		_, r := Preflight(c, PreflightRequest{
			Candidate: workflow.ModelCandidate{Harness: "claude", Model: "sonnet", Effort: "high"},
			Grant:     base.Grant, Requested: base.Requested, Prompt: "x",
		}, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalUnknownHarness || !strings.Contains(r.Detail, `"cc"`) {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("harness missing", func(t *testing.T) {
		_, r := Preflight(c, base, sourcesFor(t), failLookPath)
		if r == nil || r.Reason != RefusalHarnessMissing {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("not in catalog", func(t *testing.T) {
		_, r := Preflight(c, PreflightRequest{
			Candidate: workflow.ModelCandidate{Harness: "codex", Model: "gpt-6-astra", Effort: "high"},
			Grant:     base.Grant, Requested: base.Requested, Prompt: "x",
		}, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalNotInCatalog {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("nil catalog refuses everything", func(t *testing.T) {
		_, r := Preflight(nil, base, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalNotInCatalog {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("use not authorized", func(t *testing.T) {
		// impl-cc is trusted for authoring; a review step must refuse it —
		// the intersection of requested and trusted is empty.
		_, r := Preflight(c, PreflightRequest{
			Candidate: workflow.ModelCandidate{Harness: "cc", Model: "sonnet", Effort: "high"},
			Grant:     workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired},
			Requested: []skillregistry.Capability{skillregistry.CapReview},
			Prompt:    "x",
		}, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalUseNotAuthorized ||
			!strings.Contains(r.Detail, `missing: review`) {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("codex model unknown refuses", func(t *testing.T) {
		// An entry whose model the harness's local catalog never named: the
		// catalog authorization passes (the entry exists) and the EFFORT
		// check refuses on the unknown model path.
		c2, err := NewCatalog([]config.MachineModel{
			{Name: "ghost-codex", Harness: "codex", Model: "gpt-nonexistent", Effort: "medium", Uses: []string{"review"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, r := Preflight(c2, PreflightRequest{
			Candidate: workflow.ModelCandidate{Harness: "codex", Model: "gpt-nonexistent", Effort: "medium"},
			Grant:     base.Grant, Requested: base.Requested, Prompt: "x",
		}, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalEffortUnsupported ||
			!strings.Contains(r.Detail, "not in the local") {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("opencode read-only refuses visibly", func(t *testing.T) {
		c3, err := NewCatalog([]config.MachineModel{
			{Name: "impl-opencode", Harness: "opencode", Model: "anthropic/claude-opus-4-6", Effort: "medium", Uses: []string{"authoring"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, r := Preflight(c3, PreflightRequest{
			Candidate: workflow.ModelCandidate{Harness: "opencode", Model: "anthropic/claude-opus-4-6", Effort: "medium"},
			Grant:     workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired},
			Requested: []skillregistry.Capability{skillregistry.CapAuthoring},
			Prompt:    "x",
		}, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalReadOnlyUnsupported ||
			!strings.Contains(r.Detail, "read-only carrier") {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("cc read-only refuses visibly", func(t *testing.T) {
		// Same class as opencode: cc's only carrier is a denylist that
		// leaves the write-capable Bash, so a read_only grant refuses
		// BEFORE start — visible, typed, recorded by the chain.
		_, r := Preflight(c, PreflightRequest{
			Candidate: workflow.ModelCandidate{Harness: "cc", Model: "sonnet", Effort: "high"},
			Grant:     workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired},
			Requested: []skillregistry.Capability{skillregistry.CapAuthoring},
			Prompt:    "x",
		}, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalReadOnlyUnsupported ||
			!strings.Contains(r.Detail, "Bash") {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("invalid grant refuses", func(t *testing.T) {
		_, r := Preflight(c, PreflightRequest{
			Candidate: codexCandidate,
			Grant:     workflow.StepGrant{},
			Requested: base.Requested, Prompt: "x",
		}, sourcesFor(t), okLookPath)
		if r == nil || r.Reason != RefusalInvalidGrant {
			t.Errorf("got %+v", r)
		}
	})
}

// TestPreflightReadOnlyNeverWidensAcrossFallback is the load-bearing safety
// property: under a read_only grant, EVERY candidate that passes preflight
// carries a read-only carrier in its exact argv, and every candidate that
// cannot represent read-only — cc (denylist leaves the write-capable Bash)
// and opencode (carrier behind a fail-open agent selector) — is refused
// visibly BEFORE start. Nothing in the preflight path lets a candidate run
// wider than the grant. Fallback can narrow what runs; it can never widen
// read_only.
func TestPreflightReadOnlyNeverWidensAcrossFallback(t *testing.T) {
	c, err := NewCatalog([]config.MachineModel{
		{Name: "ro-codex", Harness: "codex", Model: "gpt-6-astra", Effort: "medium", Uses: []string{"review"}},
		{Name: "ro-cc", Harness: "cc", Model: "sonnet", Effort: "medium", Uses: []string{"review"}},
		{Name: "ro-pi", Harness: "pi", Model: "sub2api-glm/glm-5.3", Effort: "low", Uses: []string{"review"}},
		{Name: "ro-opencode", Harness: "opencode", Model: "anthropic/claude-opus-4-6", Effort: "medium", Uses: []string{"review"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired}
	used := 0
	for _, name := range []string{"ro-codex", "ro-cc", "ro-pi", "ro-opencode"} {
		entry, _ := c.Entry(name)
		ready, refusal := Preflight(c, PreflightRequest{
			Candidate: entry.Candidate, Grant: grant,
			Requested: []skillregistry.Capability{skillregistry.CapReview}, Prompt: "REVIEW",
		}, sourcesFor(t), okLookPath)
		switch name {
		case "ro-cc", "ro-opencode":
			if refusal == nil || refusal.Reason != RefusalReadOnlyUnsupported {
				t.Errorf("%s under read_only must refuse (no honest read-only carrier), got ready=%+v refusal=%+v", name, ready, refusal)
			}
			// A refusal must never leak a half-built command either: what
			// never starts cannot mutate anything.
			if ready.Command.Path != "" || len(ready.Command.Args) != 0 {
				t.Errorf("%s refusal carries a command: %+v", name, ready.Command)
			}
			continue
		default:
			if refusal != nil {
				t.Errorf("%s: unexpected refusal %+v", name, refusal)
				continue
			}
			used++
			joined := strings.Join(ready.Command.Args, " ")
			switch {
			case strings.Contains(joined, "-s read-only"):
			case strings.Contains(joined, "--tools read,grep,find,ls,"):
			default:
				t.Errorf("%s under read_only carries no read-only carrier: %q", name, joined)
			}
			if strings.Contains(joined, "workspace-write") || strings.Contains(joined, "--auto") {
				t.Errorf("%s under read_only carries a WRITE carrier: %q", name, joined)
			}
		}
	}
	if used != 2 {
		t.Errorf("expected exactly 2 ready candidates under read_only (codex sandbox, pi allowlist), got %d", used)
	}
}
