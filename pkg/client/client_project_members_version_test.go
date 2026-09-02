package client

// aihub#260, the pkg/client hop.
//
// UpdateProject forwards its body to PATCH /v1/projects/:name and looks like a
// pure passthrough, so it is tempting to assume `members_version` arrives. This
// repo's recurring defect is exactly that assumption: a parameter present in
// the MCP schema and silently dropped one hop later, which at the call site is
// indistinguishable from a precondition that passed. So read the bytes off the
// wire rather than trusting the shape of the function.
//
// No database: an httptest server is the peer.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientUpdateProjectForwardsMembersVersionOnTheWire pins that the
// compare-and-set precondition reaches the server as a JSON NUMBER under the
// key the server binds.
func TestClientUpdateProjectForwardsMembersVersionOnTheWire(t *testing.T) {
	var (
		gotPath   string
		gotMethod string
		gotBody   []byte
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotBody, _ = readAllBody(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"p","members_version":4}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "pfk_test")
	out, err := c.UpdateProject(context.Background(), "p", map[string]any{
		"members":         []map[string]any{{"user_id": "u_one", "role": "viewer"}},
		"members_version": 3,
	})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	if gotMethod != http.MethodPatch || gotPath != "/v1/projects/p" {
		t.Errorf("request was %s %s, want PATCH /v1/projects/p", gotMethod, gotPath)
	}

	// Decode rather than substring-match: `"members_version":3` as text would
	// also be satisfied by the value arriving as the string "3", which
	// domain.UpdateProjectRequest's *int would reject two hops away as an
	// opaque 400.
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("server received non-JSON body %q: %v", gotBody, err)
	}
	v, present := sent["members_version"]
	if !present {
		t.Fatalf("members_version never reached the wire; the server saw %s", gotBody)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("members_version arrived as %T (%#v), not a JSON number — the server binds it into an *int "+
			"and would answer an opaque 400", v, v)
	}
	if n != 3 {
		t.Errorf("members_version = %v on the wire, want 3", n)
	}
	if _, present := sent["members"]; !present {
		t.Errorf("members did not reach the wire alongside the version; the server saw %s", gotBody)
	}

	// The reply's new version must survive the decode, so a caller can chain a
	// second write without re-reading.
	if out["members_version"] != float64(4) {
		t.Errorf("response members_version = %#v, want 4", out["members_version"])
	}
}

// A failed compare-and-set answers 409 with details carrying the current
// version. do() folds the envelope into the error text; if it ever stopped, the
// caller would be told it conflicted but not what to retry with.
func TestClientUpdateProjectSurfacesCASConflictDetails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"CONFLICT_CAS_FAILED",` +
			`"message":"project members CAS failed: members_version is 9, not the expected 3",` +
			`"details":{"expected_members_version":3,"current_members_version":9}}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "pfk_test")
	_, err := c.UpdateProject(context.Background(), "p", map[string]any{"members_version": 3})
	if err == nil {
		t.Fatal("a 409 CONFLICT_CAS_FAILED was reported as success")
	}
	msg := err.Error()
	// Assert on the code and on the CURRENT version specifically. Matching only
	// "members_version" would be satisfied by the echo of the caller's own
	// expected value, which tells a retrying caller nothing it did not already
	// know.
	if !strings.Contains(msg, "CONFLICT_CAS_FAILED") {
		t.Errorf("error does not carry the machine-readable code: %q", msg)
	}
	if !strings.Contains(msg, `"current_members_version":9`) {
		t.Errorf("error does not carry the current version, so the caller cannot retry without a second read: %q", msg)
	}
}

func readAllBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close() //nolint:errcheck
	return io.ReadAll(r.Body)
}
