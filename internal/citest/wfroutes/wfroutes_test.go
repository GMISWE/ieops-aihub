package wfroutes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/server"
)

// registeredRoutes asks the real router what it serves. Nothing here reads Go
// source: NewRouter registers every route through echo, and echo reports them
// back, so this stays correct across group prefixes, file moves and renames.
// A nil pool is fine — registration only closes over it, handlers never run.
func registeredRoutes(t *testing.T) []Route {
	t.Helper()
	e := server.NewRouter(nil, make([]byte, 32))
	var out []Route
	for _, r := range e.Routes() {
		out = append(out, Route{Method: r.Method, Path: r.Path})
	}
	if len(out) < 20 {
		t.Fatalf("router reported %d routes; something is wrong with the oracle, not with the workflows", len(out))
	}
	return out
}

func workflowsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, ".github", "workflows")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find .github/workflows above the test's working directory")
	return ""
}

// TestWorkflowsCallOnlyRegisteredAihubEndpoints is the gate aihub#181 exists to
// leave behind. It is expected to find zero calls today; that is not the same
// as being a no-op, which is what TestScannerStillSeesTheAihub181Step is for.
func TestWorkflowsCallOnlyRegisteredAihubEndpoints(t *testing.T) {
	routes := registeredRoutes(t)
	dir := workflowsDir(t)

	calls, err := ScanDir(dir)
	if err != nil {
		t.Fatalf("scanning %s: %v", dir, err)
	}
	t.Logf("scanned %s: %d aihub call(s) against %d registered routes", dir, len(calls), len(routes))

	for _, c := range calls {
		if c.Unresolved {
			t.Errorf("workflow makes an aihub call this gate cannot check:\n  %s", c)
			continue
		}
		if !Matches(routes, c) {
			t.Errorf("workflow calls an aihub endpoint this server does not register:\n  %s\n"+
				"  Either register the route in internal/server, or delete the call. Do not wrap it in\n"+
				"  continue-on-error — that is exactly how PUT /v1/binary survived for months (aihub#181).", c)
		}
	}
}

// TestScannerStillSeesTheAihub181Step is the positive control. The fixture is
// the deleted step verbatim, so this fails if the scanner ever stops seeing the
// real defect shape — the way a gate quietly becomes decorative.
func TestScannerStillSeesTheAihub181Step(t *testing.T) {
	routes := registeredRoutes(t)

	calls, err := ScanFile(filepath.Join("testdata", "aihub181-notify-step.yml"))
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 call from the fixture, got %d: %v", len(calls), calls)
	}
	c := calls[0]
	if c.Method != "PUT" || c.Path != "/v1/binary" {
		t.Fatalf("want PUT /v1/binary, got %s %s (unresolved=%v)", c.Method, c.Path, c.Unresolved)
	}
	if c.Step != "Notify aihub (non-blocking)" {
		t.Errorf("step name not carried through: %q", c.Step)
	}
	if !strings.Contains(c.Raw, "curl") {
		t.Errorf("logical line join lost the curl command: %q", c.Raw)
	}
	if Matches(routes, c) {
		t.Fatalf("PUT /v1/binary reports as registered; the route oracle is wrong, "+
			"or a PUT route was added. Registered routes: %d", len(routes))
	}
}

// TestMatchesAcceptsRealRoutes is the negative control for the control above: a
// gate that called everything unregistered would pass the fixture test and
// still be useless. These are routes the router genuinely serves.
func TestMatchesAcceptsRealRoutes(t *testing.T) {
	routes := registeredRoutes(t)
	for _, c := range []Call{
		{Method: "GET", Path: "/v1/health"},
		{Method: "POST", Path: "/v1/work_items"},
		{Method: "PATCH", Path: "/v1/work_items/wi_uAvldPJz"}, // :id must match a real id
		{Method: "GET", Path: "/v1/memories"},
	} {
		if !Matches(routes, c) {
			t.Errorf("%s %s should match a registered route but did not", c.Method, c.Path)
		}
	}
	for _, c := range []Call{
		{Method: "PUT", Path: "/v1/work_items"},         // right path, wrong verb
		{Method: "GET", Path: "/v1/work_items/a/b/c/d"}, // too many segments
		{Method: "GET", Path: "/v1/no_such_thing"},
	} {
		if Matches(routes, c) {
			t.Errorf("%s %s should NOT match any registered route but did", c.Method, c.Path)
		}
	}
}

// TestNonLiteralPathIsReportedNotSkipped pins the decision in the package doc:
// a path built from a variable must fail loudly, or "put the path in a shell
// variable" becomes the cheapest way to get past this gate.
func TestNonLiteralPathIsReportedNotSkipped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "w.yml")
	body := "name: t\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - name: s\n        env:\n          AIHUB_URL: ${{ secrets.AIHUB_URL }}\n" +
		"        run: |\n          curl -X POST \"${AIHUB_URL}/v1/${KIND}\"\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, err := ScanFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !calls[0].Unresolved {
		t.Fatalf("want 1 unresolved call, got %+v", calls)
	}
}

// TestBaseVarDetectionSurvivesRenaming covers the second detection dimension:
// the env key can be called anything as long as its value still interpolates an
// aihub URL secret.
func TestBaseVarDetectionSurvivesRenaming(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "w.yml")
	body := "name: t\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - name: s\n        env:\n          HUB: ${{ secrets.AIHUB_URL }}\n" +
		"        run: |\n          curl -X DELETE \"${HUB}/v1/binary\"\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, err := ScanFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "DELETE" || calls[0].Path != "/v1/binary" {
		t.Fatalf("want DELETE /v1/binary via a renamed env key, got %+v", calls)
	}
}

// TestInlineSecretInScriptIsSeen covers the third: skipping the env block
// entirely by interpolating the secret straight into the run script.
func TestInlineSecretInScriptIsSeen(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "w.yml")
	body := "name: t\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - name: s\n        run: |\n" +
		"          curl -X PUT \"${{ secrets.AIHUB_URL }}/v1/binary\"\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, err := ScanFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "PUT" || calls[0].Path != "/v1/binary" {
		t.Fatalf("want PUT /v1/binary from an inline secret, got %+v", calls)
	}
}

// TestGuardLineIsNotACall keeps the gate off the `[ -z "$AIHUB_URL" ]` idiom,
// which is a guard, not an HTTP call.
func TestGuardLineIsNotACall(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "w.yml")
	body := "name: t\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - name: s\n        env:\n          AIHUB_URL: ${{ secrets.AIHUB_URL }}\n" +
		"        run: |\n          [ -z \"$AIHUB_URL\" ] && echo skip && exit 0\n" +
		"          echo \"base is ${AIHUB_URL}\"\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, err := ScanFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("guard lines should not be reported as calls, got %+v", calls)
	}
}
