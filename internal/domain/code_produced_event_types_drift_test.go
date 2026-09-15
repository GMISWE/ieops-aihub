package domain

// aihub#684 — AC6: codeProducedEventTypes must track every literal event type
// emitCodingEvent (internal/mcp/tools_coding.go) can produce. Its own header
// comment (code_produced_event_types.go) states the invariant this file
// exists to pin: "emitCodingEvent is the only producer of any of these
// three" and "this set is FORCED narrow by what the server can see" — a
// fourth call site carrying an untracked literal would widen "what the
// server can see" without anyone updating the one place the
// no-steps-recorded gate reads it from, silently growing the gate's blind
// spot beyond the two it already names on purpose.
//
// Same scan as TestEventVocabulary_CoversEveryEmitter (event_types_test.go),
// reused rather than reinvented with a second regexp: same codingRe. That
// test asks "is every emitCodingEvent literal published in EventVocabulary";
// this one asks a narrower question of the same shape of scan — "is every
// emitCodingEvent literal a key of codeProducedEventTypes" — and, because
// this map claims to BE the exact set rather than a vocabulary with
// legitimately-unemitted entries (contrast that test's "one direction only,
// deliberately"), also the converse: every key must have at least one call
// site, or it is claiming coverage of something that no longer exists.
//
// Mutant (plan §2.1): add a throwaway
// `emitCodingEvent(ctx, wiID, "force_pushed", ...)` call site anywhere under
// internal/ — found gains "force_pushed", codeProducedEventTypes does not →
// red. Revert by deleting the call site again.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeProducedEventTypesMatchesEveryEmitCodingEventCallSite(t *testing.T) {
	codingRe := regexp.MustCompile(`emitCodingEvent\(\s*[A-Za-z0-9_.]+\s*,\s*[A-Za-z0-9_.]+\s*,\s*"([a-z][a-z0-9_]*)"`)

	found := map[string][]string{}
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range codingRe.FindAllStringSubmatch(string(raw), -1) {
			found[m[1]] = append(found[m[1]], path)
		}
		return nil
	})
	require.NoError(t, err)

	// Anti-vacuity: a walk that found nothing, or a regexp that stopped
	// matching how these calls are written, would otherwise pass this test by
	// finding nothing to disagree with. codeProducedEventTypes names exactly
	// three literals today (commit, push, pr_opened) so a scan working
	// correctly must find at least that many distinct ones.
	require.GreaterOrEqual(t, len(found), 3,
		"the scan found only %d distinct emitCodingEvent literal(s) across internal/ — the SCAN is "+
			"what broke, not codeProducedEventTypes; a passing result from here would mean nothing",
		len(found))

	for typ, files := range found {
		require.True(t, codeProducedEventTypes[typ],
			"%s calls emitCodingEvent with event_type %q, which codeProducedEventTypes "+
				"(code_produced_event_types.go) does not list. aihub#684's no-steps-recorded gate reads "+
				"that map as the definition of \"this work item produced code\" — add %q there, or this "+
				"call site's completions are now invisible to the gate.",
			strings.Join(files, ", "), typ, typ)
	}

	var keys []string
	for typ := range codeProducedEventTypes {
		keys = append(keys, typ)
	}
	sort.Strings(keys)
	for _, typ := range keys {
		require.Contains(t, found, typ,
			"codeProducedEventTypes lists %q but no emitCodingEvent call site under internal/ ever "+
				"passes that literal — either the call site was deleted and this key is now dead, or "+
				"the scan no longer matches how it's written; either way the map claims coverage of "+
				"something it can no longer see", typ)
	}
}
