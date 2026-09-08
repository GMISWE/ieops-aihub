package domain

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// base_strength used to have three disjoint answers (aihub#433 / aihub#411 T1-3):
// the MCP schema published (0-1), the column CHECKs [1,5], and the Go default is
// 3.0. Nothing in Go looked at the value, so a caller who believed the published
// range got a 500 carrying the driver's constraint text.
//
// The three tests below pin the three joints of the fix, and they are separate on
// purpose: one asserts the RANGE is the column's, one asserts the DECISION about a
// violation, and one asserts WHERE that decision is taken. A single test covering
// all three would go green as soon as any one of them was right.
//
// No database: every arm here runs against a nil pool or a file on disk.
//
//	go test ./internal/domain/ -run BaseStrength -count=1

// TestBaseStrengthBoundsAreTheColumnsOwn reads the migration and requires the Go
// constants to be what the DDL enforces.
//
// This is the arm that makes the other two mean something. Bounds agreeing with
// themselves is easy; the failure this repo actually keeps hitting is two places
// stating one fact and drifting apart, which is the whole of T1-3. If a later
// migration widens or narrows the CHECK, this goes red at the constants rather
// than in production at the 21st caller.
func TestBaseStrengthBoundsAreTheColumnsOwn(t *testing.T) {
	const ddl = "../db/migrations/0006_events_memories.sql"

	src, err := os.ReadFile(ddl)
	require.NoError(t, err, "the migration that declares the column must be readable")

	// Deliberately matched on the constraint, not on a line number: the DDL is
	// formatted across two lines today and reflowing it must not silently turn
	// this gate off.
	re := regexp.MustCompile(`base_strength\s+BETWEEN\s+([0-9.]+)\s+AND\s+([0-9.]+)`)
	m := re.FindSubmatch(src)
	require.NotNil(t, m,
		"%s no longer declares a `base_strength BETWEEN lo AND hi` CHECK. Either the "+
			"constraint moved (point this test at it) or it was dropped (then "+
			"MinBaseStrength/MaxBaseStrength are enforcing a range nothing enforces). "+
			"An empty match here means the gate cannot see the constraint, not that the "+
			"constraint agrees.", ddl)

	lo, err := strconv.ParseFloat(string(m[1]), 64)
	require.NoError(t, err)
	hi, err := strconv.ParseFloat(string(m[2]), 64)
	require.NoError(t, err)

	require.Equal(t, lo, float64(MinBaseStrength),
		"MinBaseStrength disagrees with the CHECK in %s", ddl)
	require.Equal(t, hi, float64(MaxBaseStrength),
		"MaxBaseStrength disagrees with the CHECK in %s", ddl)

	// The DEFAULT is the same kind of fact and drifts the same way: whether Go or
	// Postgres supplies the value for an omitted field is an implementation
	// detail, so the two must not be able to disagree about what it is.
	dre := regexp.MustCompile(`base_strength\s+SMALLINT\s+NOT NULL\s+DEFAULT\s+([0-9.]+)`)
	dm := dre.FindSubmatch(src)
	require.NotNil(t, dm,
		"%s no longer declares a DEFAULT on base_strength; DefaultBaseStrength would then "+
			"be Go's opinion rather than the column's", ddl)
	def, err := strconv.ParseFloat(string(dm[1]), 64)
	require.NoError(t, err)
	require.Equal(t, def, float64(DefaultBaseStrength),
		"DefaultBaseStrength disagrees with the column DEFAULT in %s", ddl)
	require.GreaterOrEqual(t, float64(DefaultBaseStrength), float64(MinBaseStrength),
		"the default must itself be a legal value")
	require.LessOrEqual(t, float64(DefaultBaseStrength), float64(MaxBaseStrength),
		"the default must itself be a legal value — it was published outside the "+
			"advertised range for the whole life of this bug")
}

// TestValidateBaseStrengthRejectsWhatTheColumnWouldRefuse covers the decision:
// out of range is the CALLER's error, so 400 naming the field, never the 500 that
// the corpus recorded 13 times.
func TestValidateBaseStrengthRejectsWhatTheColumnWouldRefuse(t *testing.T) {
	legal := []*float64{nil, bsPtr(MinBaseStrength), bsPtr(2), bsPtr(3), bsPtr(MaxBaseStrength)}
	for _, v := range legal {
		if err := validateBaseStrength(v); err != nil {
			t.Errorf("base_strength %s is legal but was rejected: %v", bsShow(v), err.Message)
		}
	}

	// 0.9 / 0.8 / 0.85 / 0.1 are not invented: they are every base_strength value
	// the aihub#412 corpus observed, all of them inside the range the schema used
	// to publish. If this fix does not reject exactly those, it does not address
	// the traffic that produced the 500s.
	illegal := []float64{0, 0.1, 0.5, 0.8, 0.85, 0.9, 0.99, 5.5, 6, -1}
	for _, v := range illegal {
		err := validateBaseStrength(&v)
		require.NotNil(t, err, "base_strength %g is outside [%g,%g] and must be rejected",
			v, float64(MinBaseStrength), float64(MaxBaseStrength))
		require.Equal(t, ErrBadRequest, err.Code,
			"a caller-supplied value the server can see is wrong is a 400, not a 500 "+
				"(aihub#411 T1-6)")
		require.Equal(t, 400, err.HTTPStatus)
		// LEADS with the field, rather than merely containing it somewhere. A
		// plain Contains was tried first and a mutant walked through it: the
		// message also says "the memories.base_strength column is
		// CHECK-constrained", so dropping the field from the opening clause left
		// the assertion satisfied by an incidental mention further down. What is
		// being asserted is that a reader learns WHICH input is wrong, and that
		// is a property of the first clause, not of the string as a whole.
		require.True(t, strings.HasPrefix(err.Message, "base_strength "),
			"the message must OPEN by naming the offending field; got %q", err.Message)
		require.Contains(t, err.Message, fmt.Sprintf("%g", v),
			"the message must quote the rejected value back, so the caller does not "+
				"have to guess which of its arguments the server means; got %q", err.Message)
	}
}

// TestRememberRejectsOutOfRangeBaseStrengthBeforeThePool covers WHERE the decision
// is taken.
//
// The guard has to sit at the same point as the type checks — ahead of every
// query — because that is what makes it hold for pf_remember, pf_save_artifact and
// pf_update_memory alike (UpdateMemory builds a RememberRequest and calls
// Remember). Passing a nil pool asserts exactly that, in both directions:
//
//   - an illegal value must come back as an error with the pool never touched, and
//   - a LEGAL value must reach the pool and panic on it. That second arm is not
//     decoration: without it a guard that rejected everything would pass this test,
//     and so would one that rejected nothing if the first arm were dropped.
func TestRememberRejectsOutOfRangeBaseStrengthBeforeThePool(t *testing.T) {
	req := func(dedup string, bs *float64) *RememberRequest {
		return &RememberRequest{
			Project: "p", Type: "experience.debug", Content: "c",
			Visibility: "project", DedupMode: dedup, BaseStrength: bs,
		}
	}

	// Both dedup modes, because they reach the pool at different points and only
	// one of them is the demanding case. "off" skips textDedupCheck, so a guard
	// sitting some way down the function still looks correct under it; the
	// default ("" -> "suggest") queries immediately, which is the arm that pins
	// the guard ABOVE the first query. A mutant that moved the guard below the
	// dedup block survived while only "off" was exercised — and the default is
	// what pf_update_memory uses, since UpdateMemory builds its RememberRequest
	// without setting DedupMode at all.
	for _, dedup := range []string{"off", ""} {
		for _, v := range []float64{0.9, 0.5, 0, 5.5} {
			panicked, err := rememberWithNoPool(req(dedup, &v))
			require.Nil(t, panicked,
				"base_strength=%g with dedup_mode=%q reached the (nil) pool: the guard is "+
					"either missing or sits below the first query, where pf_update_memory "+
					"and pf_save_artifact would still get the 500", v, dedup)
			require.Error(t, err)
			var ae *AihubError
			require.ErrorAs(t, err, &ae)
			require.Equal(t, ErrBadRequest, ae.Code)
			require.True(t, strings.HasPrefix(ae.Message, "base_strength "),
				"got %q", ae.Message)
		}

		// The control. 3 is the column DEFAULT and squarely legal; it must get
		// past the guard, which with a nil pool it can only demonstrate by dying
		// on it.
		panicked, err := rememberWithNoPool(req(dedup, bsPtr(DefaultBaseStrength)))
		require.NotNil(t, panicked,
			"base_strength=%g is legal and Remember returned %v instead of proceeding to "+
				"the pool — the guard is rejecting values the column accepts",
			float64(DefaultBaseStrength), err)

		// And absent is legal too: the field is optional, and Remember defaults it.
		panicked, _ = rememberWithNoPool(req(dedup, nil))
		require.NotNil(t, panicked,
			"an omitted base_strength must be left alone for Remember to default, not rejected")
	}
}

// rememberWithNoPool calls Remember with a nil pool and reports which of the two
// possible outcomes happened. It exists because a panic escaping a test kills the
// whole package binary, which would report this file's failure as every other
// domain test's failure too.
func rememberWithNoPool(req *RememberRequest) (panicked any, err error) {
	defer func() { panicked = recover() }()
	_, _, err = Remember(context.Background(), nil, req)
	return nil, err
}

func bsPtr(v float64) *float64 { return &v }

func bsShow(v *float64) string {
	if v == nil {
		return "<omitted>"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}
