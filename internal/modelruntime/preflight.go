package modelruntime

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// PreflightRequest is everything the machine gate needs for one candidate.
// Grant and Requested come from the TRUSTED side (controller-established
// grant; the step's pinned skill contract) — never from the worker or the
// serialized flow alone.
type PreflightRequest struct {
	Candidate workflow.ModelCandidate
	// Grant is the controller's StepGrant for the step. Every candidate of
	// the step dispatches under the SAME grant; nothing candidate-specific
	// can widen it.
	Grant workflow.StepGrant
	// Requested is the step's skill contract capability set. Preflight
	// intersects it with the machine's trusted uses policy for the matching
	// catalog entry and refuses the candidate when any requested capability
	// is not authorized on this machine.
	Requested []skillregistry.Capability
	// Prompt is the step's prompt (rendered from the pinned skill bundle).
	// It is placed verbatim as the command's final argument and never parsed
	// for flags.
	Prompt string
}

// Refusal is a pre-start unavailability: the candidate never started, so
// falling to the next candidate is allowed and leaves no side effects.
// Reason is a stable machine-readable token; Detail is the human story.
type Refusal struct {
	Candidate workflow.ModelCandidate
	Reason    string
	Detail    string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("modelruntime: candidate %s/%s@%s refused before start: %s: %s",
		r.Candidate.Harness, r.Candidate.Model, r.Candidate.Effort, r.Reason, r.Detail)
}

// Stable refusal reasons. The fallback state machine and the dispatch record
// key on these tokens, so they are constants, not prose.
const (
	RefusalUnknownHarness      = "unknown_harness"
	RefusalHarnessMissing      = "harness_missing"
	RefusalNotInCatalog        = "not_in_local_catalog"
	RefusalUseNotAuthorized    = "use_not_authorized"
	RefusalEffortUnsupported   = "effort_unsupported"
	RefusalReadOnlyUnsupported = "read_only_unsupported"
	RefusalInvalidGrant        = "invalid_grant"
)

// Ready is a preflighted candidate: machine-authorized, effort-mapped, with
// its exact Command. Everything after this point is execution, not policy.
type Ready struct {
	Candidate workflow.ModelCandidate
	// Entry is the machine catalog entry that authorized the candidate.
	Entry Entry
	// Command is the exact invocation; effort fragments included.
	Command Command
	// Effort records requested/native/support and the honest verification
	// status for the dispatch record.
	Effort EffortSupport
}

// Preflight is the machine gate every new-path candidate must pass BEFORE it
// may start, in check order:
//
//  1. harness vocabulary (unknown key — including "claude" for Claude Code —
//     refuses with the right spelling);
//  2. binary presence (exec.LookPath via the injectable resolver);
//  3. allowed: the exact candidate triple matches a [[models]] entry — the
//     machine's trusted authorization surface. No match, no dispatch;
//  4. capability intersection: every requested capability must be in the
//     entry's uses. The EFFECTIVE capability is the intersection of what the
//     step requests and what the machine trusts this entry for — a review
//     step on an entry trusted for authoring only refuses, and no fallback
//     candidate can widen read_only because the grant applies to every
//     candidate identically;
//  5. effort support: the uniform adapter maps the effort onto this
//     harness's actual invocation flags, consulting per-model metadata where
//     the harness exposes it locally, and refusing unknown or unsupported
//     paths instead of guessing;
//  6. grant representability: a read_only grant needs a representable
//     read-only carrier. cc (whose only carrier is a denylist that leaves
//     the write-capable Bash) and opencode (whose carrier lives behind a
//     fail-open agent selector) have none and are refused visibly rather
//     than dispatched wide.
//
// A refusal is pre-start unavailability by construction: nothing has run, so
// the caller may fall to the next candidate (through the Fallback machine,
// which bounds how often).
//
// lookPath is injectable so tests never depend on installed binaries; nil
// means exec.LookPath. sources supplies the harness-local model catalogs
// (nil fields mean "no local metadata source"; pi/codex/opencode then refuse
// rather than assume).
func Preflight(catalog *Catalog, req PreflightRequest, sources CatalogSources, lookPath func(string) (string, error)) (Ready, *Refusal) {
	c := req.Candidate
	detail := func(reason, d string) (Ready, *Refusal) {
		return Ready{}, &Refusal{Candidate: c, Reason: reason, Detail: d}
	}

	// 1. harness vocabulary.
	binary, known := HarnessBinary[c.Harness]
	if !known {
		spelling := ""
		if c.Harness == "claude" || c.Harness == "claude-code" {
			spelling = ` (Claude Code's harness key is spelled "cc")`
		}
		return detail(RefusalUnknownHarness,
			fmt.Sprintf("harness %q is not one of cc, codex, opencode, pi%s", c.Harness, spelling))
	}

	// 2. binary presence.
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(binary)
	if err != nil {
		return detail(RefusalHarnessMissing,
			fmt.Sprintf("%s binary %q not found on PATH: %v", c.Harness, binary, err))
	}

	// 3. allowed: exact catalog authorization.
	if catalog == nil {
		return detail(RefusalNotInCatalog,
			"this machine has no [[models]] catalog loaded; every new-path candidate is refused rather than run on trust authored elsewhere")
	}
	entry, ok := catalog.Match(c)
	if !ok {
		return detail(RefusalNotInCatalog,
			fmt.Sprintf("no [[models]] entry pins harness=%q model=%q effort=%q exactly. Available: %s",
				c.Harness, c.Model, c.Effort, strings.Join(catalogDescriptions(catalog, c.Harness), "; ")))
	}

	// 4. capability intersection with the machine's trusted uses policy.
	if missing := missingUses(entry, req.Requested); len(missing) > 0 {
		return detail(RefusalUseNotAuthorized,
			fmt.Sprintf("entry %q is trusted for uses [%s] but the step requests [%s] (missing: %s)",
				entry.Name, strings.Join(entry.UsesRaw, ", "), capabilityStrings(req.Requested),
				strings.Join(missing, ", ")))
	}

	// 5. uniform effort support (per-model where locally readable).
	support, err := EffortFor(c.Harness, c.Model, c.Effort, sources)
	if err != nil {
		return detail(RefusalEffortUnsupported, err.Error())
	}
	if !support.Supported {
		return detail(RefusalEffortUnsupported, support.Refusal)
	}

	// 6. grant representability + command construction. BuildCommand carries
	// the grant's capability and isolation flags; it refuses a read_only
	// grant on a harness with no representable carrier with a typed error
	// (ErrNoReadOnlyCarrier: cc and opencode today), classified here so the
	// refusal reason stays stable even as the set of refusing harnesses
	// grows.
	//
	// NOTE on isolation: producer isolation is representable on all four
	// harnesses (a fresh subprocess with its own session: codex --ephemeral,
	// pi --no-session, cc/opencode new-session defaults), so there is no
	// isolation refusal today. If a future harness cannot run isolated, the
	// refusal belongs HERE, before anything starts.
	cmd, err := BuildCommand(c, req.Grant, support, req.Prompt)
	if err != nil {
		if errors.Is(err, ErrNoReadOnlyCarrier) {
			return detail(RefusalReadOnlyUnsupported, err.Error())
		}
		switch req.Grant.Authority {
		case workflow.AuthorityReadOnly, workflow.AuthorityWrite:
		default:
			return detail(RefusalInvalidGrant, err.Error())
		}
		switch req.Grant.ProducerIsolation {
		case workflow.IsolationShared, workflow.IsolationRequired:
		default:
			return detail(RefusalInvalidGrant, err.Error())
		}
		// The remaining reachable failure is an unsupported effort/carrier
		// combination surfaced at construction time.
		return detail(RefusalEffortUnsupported, err.Error())
	}

	ready := Ready{
		Candidate: c,
		Entry:     entry,
		Command:   cmd,
		Effort:    support,
	}
	ready.Command.Path = path
	return ready, nil
}

// missingUses returns the requested capabilities the entry does not trust.
func missingUses(entry Entry, requested []skillregistry.Capability) []string {
	trusted := make(map[skillregistry.Capability]bool, len(entry.Uses))
	for _, u := range entry.Uses {
		trusted[u] = true
	}
	var missing []string
	seen := make(map[skillregistry.Capability]bool, len(requested))
	for _, r := range requested {
		if seen[r] {
			continue
		}
		seen[r] = true
		if !trusted[r] {
			missing = append(missing, string(r))
		}
	}
	sort.Strings(missing)
	return missing
}

func capabilityStrings(caps []skillregistry.Capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = string(c)
	}
	sort.Strings(out)
	return out
}

func catalogDescriptions(catalog *Catalog, harness string) []string {
	var out []string
	for _, e := range catalog.Entries() {
		if harness == "" || e.Candidate.Harness == harness {
			out = append(out, e.CatalogLine)
		}
	}
	if len(out) == 0 && harness != "" {
		out = append(out, fmt.Sprintf("(no [[models]] entries for harness %q)", harness))
	}
	return out
}
