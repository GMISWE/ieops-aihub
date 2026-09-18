package modelruntime

import (
	"fmt"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// Entry is one validated [[models]] route: the raw config entry, the resolved
// harness-native candidate it pins, and the trusted uses policy. Immutable
// once the Catalog is built.
type Entry struct {
	Name        string
	Candidate   workflow.ModelCandidate
	Uses        []skillregistry.Capability
	UsesRaw     []string
	CatalogLine string // human-readable provenance for error messages
}

// Catalog is the validated, lookup-optimised view of a machine's [[models]]
// entries. Built via NewCatalog / LoadCatalog; both refuse an invalid catalog
// (config.ValidateModels) so nothing downstream ever sees a half-valid entry.
type Catalog struct {
	entries     []Entry
	byName      map[string]int
	byCandidate map[string]int
}

// NewCatalog validates models and returns the lookup view. An error from
// config.ValidateModels is passed through verbatim — the operator sees the
// whole problem list, not a first-failure truncation.
func NewCatalog(models []config.MachineModel) (*Catalog, error) {
	if err := config.ValidateModels(models); err != nil {
		return nil, err
	}
	c := &Catalog{
		entries:     make([]Entry, 0, len(models)),
		byName:      make(map[string]int, len(models)),
		byCandidate: make(map[string]int, len(models)),
	}
	for i, m := range models {
		e := Entry{
			Name:      m.Name,
			Candidate: workflow.ModelCandidate{Harness: m.Harness, Model: m.Model, Effort: m.Effort},
			UsesRaw:   append([]string(nil), m.Uses...),
		}
		for _, u := range m.Uses {
			// ValidateModels already refused anything outside the closed set.
			e.Uses = append(e.Uses, skillregistry.Capability(u))
		}
		e.CatalogLine = fmt.Sprintf("[[models]] %q: harness=%q model=%q effort=%q uses=[%s]",
			m.Name, m.Harness, m.Model, m.Effort, strings.Join(m.Uses, ","))
		c.entries = append(c.entries, e)
		c.byName[m.Name] = i
		// Duplicate-triple guard at the exact site the reviewer flagged: the
		// by-candidate map must never silently overwrite. ValidateModels
		// (called above) already refuses duplicates, so this is an
		// unreachable-by-design invariant — kept so that any future drift
		// between validation and this key construction fails LOUD here
		// instead of quietly routing dispatch authorization to whichever
		// entry happened to be indexed last (and to ITS uses policy).
		key := e.key()
		if first, dup := c.byCandidate[key]; dup {
			return nil, fmt.Errorf(
				"modelruntime: [[models]] %q duplicates the candidate triple of %q (harness=%q model=%q effort=%q); "+
					"the route map must not overwrite — this is unreachable unless validation and key construction drifted",
				m.Name, c.entries[first].Name, m.Harness, m.Model, m.Effort)
		}
		c.byCandidate[key] = i
	}
	return c, nil
}

// LoadCatalog loads and validates the catalog from a MachineConfig.
func LoadCatalog(mc *config.MachineConfig) (*Catalog, error) {
	var models []config.MachineModel
	if mc != nil {
		models = mc.Models
	}
	return NewCatalog(models)
}

func (e Entry) key() string {
	return e.Candidate.Harness + "\x00" + e.Candidate.Model + "\x00" + e.Candidate.Effort
}

// Entries returns the catalog in file order (deterministic; operator-visible
// order is stable across reloads).
func (c *Catalog) Entries() []Entry {
	return append([]Entry(nil), c.entries...)
}

// Entry returns the route named name. Exact match only: there is deliberately
// no prefix, suffix or fuzzy path, because a near-miss resolving silently to
// a different provider is exactly the aihub#676 failure class.
func (c *Catalog) Entry(name string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	i, ok := c.byName[name]
	if !ok {
		return Entry{}, false
	}
	return c.entries[i], true
}

// Match returns the catalog entry pinning exactly this candidate triple.
// This is the dispatch-time authorization check ("allowed"): a step candidate
// that does not exactly match a machine catalog entry is not one this machine
// authorized, and preflight refuses it rather than running it on trust
// authored elsewhere.
//
// Exact means exact: the same harness+model at a different effort is a
// different route and does not match — silently dispatching a pinned "high"
// candidate at an entry configured "medium" would be an effort downgrade by
// alias, the failure the spec's "no silent downgrade" clause exists for.
func (c *Catalog) Match(candidate workflow.ModelCandidate) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	i, ok := c.byCandidate[candidate.Harness+"\x00"+candidate.Model+"\x00"+candidate.Effort]
	if !ok {
		return Entry{}, false
	}
	return c.entries[i], true
}

// Candidates resolves ordered catalog names into ordered model candidates for
// a new-flow step's Models field. Deterministic and exact:
//
//   - output order is the order of names, no sorting, no dedup;
//   - an unknown name is an error listing the available names — never a
//     partial result and never a guess;
//   - a repeated name is an error: the step shape (workflow.Validate) rejects
//     duplicate candidate triples later with a worse message, and a repeated
//     name in a priority list is almost certainly an authoring slip.
//
// The resolved triples, not the names, are what a WI persists: "step
// candidates persist resolved harness-native identity" (spec D9). Names are
// machine-local convenience; the pinned identity survives catalog edits, and
// a dispatch machine whose catalog does not carry the exact triple refuses it
// at preflight instead of re-resolving the name against its own routes.
func (c *Catalog) Candidates(names []string) ([]workflow.ModelCandidate, error) {
	if c == nil {
		return nil, fmt.Errorf("modelruntime: nil catalog")
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("modelruntime: resolving an empty name list; a step needs at least one candidate")
	}
	out := make([]workflow.ModelCandidate, 0, len(names))
	seen := make(map[string]bool, len(names))
	for i, name := range names {
		e, ok := c.Entry(name)
		if !ok {
			return nil, fmt.Errorf(
				"modelruntime: model name %d %q is not in the local [[models]] catalog. Available: %s",
				i, name, strings.Join(c.names(), ", "))
		}
		if seen[name] {
			return nil, fmt.Errorf(
				"modelruntime: name %q appears twice in the candidate list; order is the fallback order, a repeat is an authoring slip",
				name)
		}
		seen[name] = true
		out = append(out, e.Candidate)
	}
	return out, nil
}

func (c *Catalog) names() []string {
	out := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.Name)
	}
	return out
}
