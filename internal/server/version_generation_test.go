package server

// aihub#416 AC-8: GET /v1/version carries a PROCESS component, so a service's
// "generation" changes when the process is replaced — including by a restart of
// the same image.
//
// ─── Why both halves have to be asserted ───────────────────────────────────
//
// 🔴 "started_at differs between two runs" is satisfied by a field that returns
// a fresh random value on EVERY REQUEST, and such a field would invalidate every
// observation ever made — the opposite failure, equally silent. So the test
// asserts a CONJUNCTION over two processes of the same binary:
//
//	git_commit  SAME     — it is a build-time value and the build did not change
//	started_at  DIFFERENT — it is a process-time value and the process did
//
// plus a third arm inside one process: two requests to the SAME server must
// return the SAME started_at. Without it, "different across processes" cannot
// distinguish a process identity from a request counter.
//
// ─── Why a real subprocess ─────────────────────────────────────────────────
//
// version.ProcessStartTime is a package variable initialised once. Two servers
// started in one test binary share it, so an in-process test could only ever
// assert the field's presence — it could not observe the thing the field is FOR.
// This re-executes the test binary with a marker env var; the child answers one
// request against the real handler and prints the payload.
//
// The child runs `-test.run` against this same function, so there is exactly one
// binary and one build, which is what makes the git_commit half meaningful.
//
// No database: handleVersion is registered before the authed group and touches
// no pool.
//
// MUTANT: delete the `"started_at"` line from handleVersion — the first arm goes
// red on the missing key. Replace version.ProcessStartTime with
// `time.Now().UTC()` evaluated per request — the same-process arm goes red while
// the cross-process arm stays green, which is the pair that makes this a
// conjunction rather than a coin flip.
//
// Run: go test ./internal/server/ -run TestVersionCarriesAProcessGeneration -v

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// versionProbeEnv makes a re-executed test binary answer one /v1/version request
// and exit instead of running the suite.
const versionProbeEnv = "AIHUB_VERSION_PROBE_416"

// versionPayload performs GET /v1/version against the real handler in THIS
// process and returns the decoded body.
func versionPayload(t *testing.T) map[string]any {
	t.Helper()
	e := echo.New()
	e.GET("/v1/version", handleVersion())

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/version = %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /v1/version body %q: %v", rec.Body.String(), err)
	}
	return out
}

// versionPayloadFromChild re-executes this test binary and returns the payload
// the child process reports.
func versionPayloadFromChild(t *testing.T) map[string]any {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestVersionCarriesAProcessGeneration")
	cmd.Env = append(os.Environ(), versionProbeEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("re-exec the test binary as a version probe: %v (output %q)", err, out)
	}
	// The child prints the payload on its own line, prefixed, so `go test`'s own
	// output around it cannot be mistaken for the body.
	const marker = "VERSION_PROBE_416 "
	line := ""
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, marker) {
			line = strings.TrimPrefix(l, marker)
		}
	}
	if line == "" {
		t.Fatalf("the child produced no %sline; output was %q", marker, out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("decode child payload %q: %v", line, err)
	}
	return payload
}

func TestVersionCarriesAProcessGeneration(t *testing.T) {
	// The child half of the re-exec. It must run before anything else so the
	// child does no work beyond answering one request.
	if os.Getenv(versionProbeEnv) == "1" {
		body, err := json.Marshal(versionPayload(t))
		if err != nil {
			t.Fatalf("marshal probe payload: %v", err)
		}
		//nolint:forbidigo // the parent process parses this line
		os.Stdout.WriteString("VERSION_PROBE_416 " + string(body) + "\n") //nolint:errcheck
		return
	}

	mine := versionPayload(t)

	t.Run("the payload carries started_at", func(t *testing.T) {
		got, ok := mine["started_at"].(string)
		if !ok || got == "" {
			t.Fatalf("/v1/version has no started_at: %v. Without a process component every field in "+
				"this response is a build-time constant, so a service that was RESTARTED reports the "+
				"same generation it did before — and an observation that spanned the restart is "+
				"reported valid. A probe that fails green is worse than no probe", mine)
		}
		// Parsed, not merely non-empty: a probe expression splits on this value,
		// and an unparseable one would be compared as an opaque string forever.
		if _, err := time.Parse(time.RFC3339Nano, got); err != nil {
			t.Errorf("started_at %q does not parse as RFC3339Nano: %v", got, err)
		}
		// ⚠️ NOT asserted: that `version` is useful. It is the constant "dev" in
		// production because CI's main-branch image build passes no VERSION
		// build-arg, which is exactly why the generation expression in the
		// scenario repo uses git_commit and started_at and not this field.
		if _, ok := mine["version"]; !ok {
			t.Error("the response lost `version`; this test does not use it, but withdrawing a " +
				"published field is a contract change that should not happen by accident")
		}
	})

	t.Run("the same process answers the same started_at twice", func(t *testing.T) {
		// The arm that separates "identifies this process" from "returns a fresh
		// value per request". A per-request value would pass the cross-process arm
		// below and invalidate every observation ever made.
		again := versionPayload(t)
		if mine["started_at"] != again["started_at"] {
			t.Errorf("two requests to one process reported different started_at (%v vs %v). "+
				"The value would then differ between the OPEN and CLOSE reads of every observation, "+
				"so every conclusion would invalidate itself",
				mine["started_at"], again["started_at"])
		}
	})

	t.Run("a second process of the same build reports the same commit and a different start", func(t *testing.T) {
		theirs := versionPayloadFromChild(t)

		if mine["git_commit"] != theirs["git_commit"] {
			t.Fatalf("two processes of ONE binary reported different git_commit (%v vs %v) — the "+
				"fixture is not comparing one build with itself, so the started_at assertion below "+
				"would not be measuring a restart", mine["git_commit"], theirs["git_commit"])
		}
		if mine["build_time"] != theirs["build_time"] {
			t.Errorf("build_time differs between two processes of one binary (%v vs %v)",
				mine["build_time"], theirs["build_time"])
		}
		if mine["started_at"] == theirs["started_at"] {
			t.Errorf("two processes of the same build reported the SAME started_at (%v). Every other "+
				"field in this response comes from build-time ldflags and is byte-identical across a "+
				"restart, so with this one equal too a generation probe cannot detect that the "+
				"service it observed was replaced — which is the false-green direction",
				mine["started_at"])
		}
	})
}
