package domain

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
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

// TestBaseStrengthIsTruncatedByThePgxInt2Codec is aihub#475's measurement, kept
// executable.
//
// aihub#459 recorded the coercion mechanism as UNVERIFIED and named two
// candidates: "(a) pgx refuses to encode" or "(b) the value is coerced
// silently". It is (b), the direction is TOWARD ZERO, and there is no error at
// any value — so the three tests above, which pin the RANGE, do not between them
// pin what actually reaches the column. A value can satisfy every one of them
// and still be stored as a different number.
//
// This is a CHARACTERISATION test, and its subject is a dependency rather than
// this repo's own code, which is the point: the truncation is the only reason
// handleReinforceMemory and Remember must report the value they read back
// instead of the one they computed. If a pgx upgrade turns it into an error, or
// a QueryExecMode change routes the literal to the server (where the rule is
// round-half-to-even, not truncate — 4.5 would become 4 but 3.5 would become 4
// too), the justification for those RETURNING clauses has changed and this goes
// red where the reasoning lives.
//
// It does NOT assert that truncation is desirable. aihub#459 owns that.
func TestBaseStrengthIsTruncatedByThePgxInt2Codec(t *testing.T) {
	// Premise 1: the column really is int2. If aihub#459 is settled by widening
	// the column, this whole test is about the wrong codec and must be re-derived
	// rather than left green against an OID nothing writes any more.
	const ddl = "../db/migrations/0006_events_memories.sql"
	src, err := os.ReadFile(ddl)
	require.NoError(t, err)
	require.Regexp(t, `base_strength\s+SMALLINT`, string(src),
		"%s no longer declares base_strength SMALLINT. This test characterises pgx's "+
			"int2 codec because that is the codec the column forces; against any other "+
			"column type it is measuring something the write path never uses.", ddl)

	// Premise 2: the pool takes no QueryExecMode override, so params are encoded
	// client-side through the described OID (the extended protocol's default)
	// rather than being interpolated as literals for the server to round.
	poolSrc, err := os.ReadFile("../db/db.go")
	require.NoError(t, err)
	require.NotContains(t, string(poolSrc), "QueryExecMode",
		"internal/db/db.go now sets a QueryExecMode. Under SimpleProtocol the literal "+
			"reaches Postgres and is ROUNDED half-to-even instead of truncated, so the "+
			"table below stops describing what aihub does. Re-measure before editing it.")

	m := pgtype.NewMap()
	encode := func(t *testing.T, v float64, format int16) string {
		t.Helper()
		buf, encErr := m.Encode(pgtype.Int2OID, format, v, nil)
		// The (a) arm of aihub#459's two candidates, refuted here rather than
		// assumed: an error at ANY of these values would mean the caller does get
		// a signal, and the whole "silent no-op" defect would not exist.
		require.NoError(t, encErr,
			"pgx now REFUSES base_strength=%g rather than coercing it. That is a better "+
				"behaviour, not a worse one — but it is a different one, and the "+
				"handlers' error paths were written for a value that always encodes.", v)
		var back float64
		require.NoError(t, m.Scan(pgtype.Int2OID, format, buf, &back))
		return strconv.FormatFloat(back, 'g', -1, 64)
	}

	// Every row measured at the pinned pgx version. The two out-of-range rows are
	// kept because they are the mechanism behind aihub#412's 13 INTERNAL_ERRORs:
	// 0.9 is inside the range the schema used to publish and encodes to 0, which
	// is what memories_base_strength_check refused.
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{3.0, "3"},   // control: an integral value must survive untouched
		{5.0, "5"},   // control: MaxBaseStrength
		{1.0, "1"},   // control: MinBaseStrength
		{3.5, "3"},   // NOT 4 — toward zero, not nearest
		{4.5, "4"},   // NOT 4 by rounding half-to-even; by truncation
		{4.999, "4"}, // the whole fractional part is dropped
		{1.9, "1"},
		{0.9, "0"},  // the corpus value; 0 violates the CHECK
		{-0.5, "0"}, // toward zero from below, so -0.5 is 0 and not -1
	} {
		for _, f := range []struct {
			name   string
			format int16
		}{
			{"binary", pgtype.BinaryFormatCode},
			{"text", pgtype.TextFormatCode},
		} {
			got := encode(t, tc.in, f.format)
			require.Equal(t, tc.want, got,
				"pgx %s encoding of base_strength=%g reached the column as %s, expected %s. "+
					"Both formats are checked because a codec that changed in only one of "+
					"them would still be a change in what gets stored.", f.name, tc.in, got, tc.want)
		}
	}

	// The consequence, stated as the assertion the handlers depend on: a delta
	// under 1 applied to a stored value is a no-op. That is why
	// handleReinforceMemory must answer with the row's value — reporting the
	// arithmetic would claim a change that did not happen, on every call, forever.
	require.Equal(t, "3", encode(t, DefaultBaseStrength+0.5, pgtype.BinaryFormatCode),
		"3 + 0.5 must still store 3; if it does not, the aihub#475 defect no longer "+
			"has the mechanism its fix was written for")
}

// ─────────────────────────── aihub#459: integrality ──────────────────────────
//
// The three tests above pin the RANGE and the fourth characterises what pgx does
// to a value inside it. aihub#459 is the disposition the fourth one deliberately
// did not take: the owner ruled on 2026-09-09 that a non-integral strength is
// REFUSED, rather than rounded in Go or made storable by widening the column.
//
// Same split as above, and for the same reason: one test asserts the RULE, one
// asserts that the create path is WIRED to it, and one asserts the rule's
// PRECEDENCE against the range check — because a value can violate both, and
// which error it gets is a fact about this code that a reader should not have to
// re-derive.

// TestValidateIntegralStrengthIsTheWholeNumberRule covers the rule itself.
//
// It is exported and shared, so the arms below check the two things a shared
// rule can get wrong that a private one cannot: that it names the caller's OWN
// field rather than a hard-coded one, and that it is the same answer for both.
func TestValidateIntegralStrengthIsTheWholeNumberRule(t *testing.T) {
	// Negative and zero are included on purpose. base_strength can never be
	// either (the range check runs first), but strength_delta is an ADDEND and
	// -1 is an entirely ordinary one, so a rule that only understood the
	// base_strength side would reject legal reinforce traffic. Negative zero is
	// built with math.Copysign rather than written as -0.0, which Go folds to
	// plain 0 at compile time — the literal would test nothing (staticcheck
	// SA4026 says so), and math.Trunc(-0) is -0, which must be accepted.
	negZero := math.Copysign(0, -1)
	for _, v := range []float64{-5, -1, 0, 1, 2, 3, 4, 5, 42, negZero} {
		require.Nil(t, ValidateIntegralStrength("strength_delta", v),
			"%g is a whole number and must be accepted", v)
		require.Nil(t, ValidateIntegralStrength("base_strength", v),
			"%g is a whole number and must be accepted", v)
	}

	for _, v := range []float64{0.5, -0.5, 2.5, 1.1, 4.999, 3.0000001, -1.5} {
		err := ValidateIntegralStrength("strength_delta", v)
		require.NotNil(t, err, "%g is not a whole number and must be refused", v)
		require.Equal(t, ErrBadRequest, err.Code,
			"a value the server can see is wrong is the caller's error (aihub#411 T1-6)")
		require.Equal(t, 400, err.HTTPStatus)
		// The field the CALLER named, leading the message — the same property the
		// range check is held to above, and the one a shared helper is most
		// likely to lose, by hard-coding whichever field it was written for.
		require.True(t, strings.HasPrefix(err.Message, "strength_delta "),
			"the message must OPEN by naming the offending field; got %q", err.Message)
		require.Contains(t, err.Message, fmt.Sprintf("%g", v),
			"the message must quote the rejected value back; got %q", err.Message)

		other := ValidateIntegralStrength("base_strength", v)
		require.NotNil(t, other)
		require.True(t, strings.HasPrefix(other.Message, "base_strength "),
			"got %q", other.Message)
	}

	// The control on the control: the two messages must differ ONLY in the field
	// name. A helper that quietly said "base_strength" in the body of a
	// strength_delta rejection would satisfy every prefix assertion above and
	// still send the caller to the wrong argument.
	a := ValidateIntegralStrength("strength_delta", 2.5)
	b := ValidateIntegralStrength("base_strength", 2.5)
	require.Equal(t,
		strings.TrimPrefix(a.Message, "strength_delta"),
		strings.TrimPrefix(b.Message, "base_strength"),
		"the two rejections must be the same sentence about the same rule")
}

// TestValidateBaseStrengthRefusesFractionalInRangeValues covers the arm the
// range tests above cannot reach: a value they call legal.
//
// 4.5 satisfies every assertion in
// TestValidateBaseStrengthRejectsWhatTheColumnWouldRefuse — it is inside
// [1,5] — and before aihub#459 it was admitted, encoded through pgx's int2
// codec, and stored as 4. That is the gap this arm closes, so the values here
// are deliberately ones the range check would wave through.
func TestValidateBaseStrengthRefusesFractionalInRangeValues(t *testing.T) {
	for _, v := range []float64{1.5, 2.5, 3.5, 4.5, 4.999, 1.0000001} {
		err := validateBaseStrength(&v)
		require.NotNil(t, err,
			"base_strength %g is inside [%g,%g] but not a whole number: the column is "+
				"SMALLINT, so admitting it means answering 200 having stored %g",
			v, float64(MinBaseStrength), float64(MaxBaseStrength), math.Trunc(v))
		require.Equal(t, ErrBadRequest, err.Code)
		require.True(t, strings.HasPrefix(err.Message, "base_strength "),
			"got %q", err.Message)
		require.Contains(t, err.Message, "whole number",
			"the message must say WHY the value is refused, or a caller reading it "+
				"alongside the range message cannot tell the two rejections apart; got %q",
			err.Message)
	}

	// PRECEDENCE, and it is a negative control on the change rather than a new
	// requirement: every base_strength the aihub#412 corpus actually carried
	// (0.9 x9, 0.8 x2, 0.85, 0.1) violates BOTH rules, and each must still get
	// the RANGE error it got before this work item. The range is the constraint
	// the caller hit first and the one the column would have refused outright;
	// re-answering those 13 calls with "not a whole number" would be a true
	// sentence that points at the smaller of two problems.
	for _, v := range []float64{0.9, 0.8, 0.85, 0.1, 5.5, -0.5} {
		err := validateBaseStrength(&v)
		require.NotNil(t, err)
		require.Contains(t, err.Message, "is out of range",
			"base_strength %g is out of range AND fractional; the range error is the one "+
				"it took before aihub#459 and must remain the one it takes; got %q",
			v, err.Message)
	}
}

// TestRememberRefusesFractionalBaseStrengthBeforeThePool covers WHERE the new
// decision is taken, and is the integrality twin of
// TestRememberRejectsOutOfRangeBaseStrengthBeforeThePool above.
//
// A rule with no caller is the state this repo was in for exactly as long as
// aihub#459 was open: the truncation was measured, written down in three
// comments and a characterisation test, and nothing refused anything. Asserting
// the rule alone would go green in that state, so this asserts the wiring — and
// against a nil pool, which additionally pins the guard ABOVE the first query,
// where it has to be for pf_update_memory and pf_save_artifact to inherit it.
func TestRememberRefusesFractionalBaseStrengthBeforeThePool(t *testing.T) {
	req := func(dedup string, bs *float64) *RememberRequest {
		return &RememberRequest{
			Project: "p", Type: "experience.debug", Content: "c",
			Visibility: "project", DedupMode: dedup, BaseStrength: bs,
		}
	}

	// Both dedup modes, for the reason the range test states: "off" skips
	// textDedupCheck and so does not pin the guard's position, while the default
	// queries immediately — and the default is what pf_update_memory uses.
	for _, dedup := range []string{"off", ""} {
		for _, v := range []float64{2.5, 4.5, 1.5, 4.999} {
			panicked, err := rememberWithNoPool(req(dedup, &v))
			require.Nil(t, panicked,
				"base_strength=%g with dedup_mode=%q reached the (nil) pool: an in-range "+
					"fractional value is still being handed to the int2 codec, which "+
					"truncates it toward zero and stores %g under a 200",
				v, dedup, math.Trunc(v))
			require.Error(t, err)
			var ae *AihubError
			require.ErrorAs(t, err, &ae)
			require.Equal(t, ErrBadRequest, ae.Code)
			require.Contains(t, ae.Message, "whole number", "got %q", ae.Message)
		}

		// The control, restated here rather than inherited: 3 is the column
		// DEFAULT and a whole number, and it must reach the pool. Without this
		// arm a guard that refused every base_strength would pass the loop above.
		panicked, err := rememberWithNoPool(req(dedup, bsPtr(DefaultBaseStrength)))
		require.NotNil(t, panicked,
			"base_strength=%g is a whole number inside the range and Remember returned %v "+
				"instead of proceeding to the pool", float64(DefaultBaseStrength), err)
	}
}
