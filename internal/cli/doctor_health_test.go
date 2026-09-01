package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// The bodies below are written as JSON and decoded, rather than built as Go
// maps, precisely because the bug this guards against lives in the decode: a
// key that is ABSENT and a key that is explicitly `false` are the same zero
// value once they reach Go, and reading them as plain bools makes every server
// older than aihub#316 look like it has a dead embedding backend. A map literal
// would let the test author "omit" a field in a way the real wire format never
// does.
func decodeHealth(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("bad test fixture %q: %v", body, err)
	}
	return m
}

func TestHealthVerdict(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus string
		wantIn     []string // substrings the message must carry
		wantNotIn  []string
	}{
		{
			name:       "new server, everything up",
			body:       `{"status":"ok","version":"1.2.3","db_ok":true,"embedding_enabled":true,"embedding_ok":true}`,
			wantStatus: "ok",
			wantIn:     []string{"aihub reachable"},
			wantNotIn:  []string{"degraded", "predates"},
		},
		{
			name:       "new server, embedding backend timing out",
			body:       `{"status":"degraded","version":"1.2.3","db_ok":true,"embedding_enabled":true,"embedding_ok":false,"embedding_error_kind":"timeout"}`,
			wantStatus: "warning",
			wantIn:     []string{"degraded", "embedding", "timeout"},
			wantNotIn:  []string{"database"},
		},
		{
			name:       "new server, embedding unreachable",
			body:       `{"status":"degraded","version":"1.2.3","db_ok":true,"embedding_enabled":true,"embedding_ok":false,"embedding_error_kind":"unreachable"}`,
			wantStatus: "warning",
			wantIn:     []string{"embedding", "unreachable"},
		},
		{
			name:       "new server, database ping failing",
			body:       `{"status":"degraded","version":"1.2.3","db_ok":false,"embedding_enabled":true,"embedding_ok":true}`,
			wantStatus: "warning",
			wantIn:     []string{"database", "db_ok=false"},
		},
		{
			name:       "new server, embedding switched off is not a degradation",
			body:       `{"status":"ok","version":"1.2.3","db_ok":true,"embedding_enabled":false,"embedding_ok":false}`,
			wantStatus: "ok",
			wantIn:     []string{"embedding disabled"},
		},
		{
			// The regression this whole *bool business exists for. No embedding
			// keys at all: a missing key must read as "this server does not
			// report it", never as false, or doctor warns on every older server.
			name:       "old server, no embedding fields at all",
			body:       `{"status":"ok","version":"0.9.0","db_ok":true}`,
			wantStatus: "ok",
			wantIn:     []string{"predates aihub#316"},
			wantNotIn:  []string{"degraded", "probe failed"},
		},
		{
			// Old servers hardcoded status:"ok" — it did not even reflect db_ok —
			// so the status field alone cannot be trusted in either direction.
			name:       "old server whose hardcoded ok contradicts its own db_ok",
			body:       `{"status":"ok","version":"0.9.0","db_ok":false}`,
			wantStatus: "warning",
			wantIn:     []string{"database", "db_ok=false"},
			wantNotIn:  []string{"embedding backend: probe failed"},
		},
		{
			// db_ok absent entirely. Reading it as a plain bool would make this
			// "the database ping failed" — the missing-key-is-false trap on the
			// one field that has been in this body since before aihub#316.
			name:       "field absent is not the same as field false",
			body:       `{"status":"ok","version":"1.2.3"}`,
			wantStatus: "ok",
			wantNotIn:  []string{"database", "db_ok=false"},
		},
		{
			// A future server degrading on something with no field here must not
			// be reported green just because the fields we know about are fine.
			name:       "newer server degraded on a dependency this client has no field for",
			body:       `{"status":"degraded","version":"9.9.9","db_ok":true,"embedding_enabled":true,"embedding_ok":true}`,
			wantStatus: "warning",
			wantIn:     []string{"status=degraded"},
		},
		{
			name:       "field present but not a boolean is unreadable, not false",
			body:       `{"status":"ok","version":"1.2.3","db_ok":"yes","embedding_enabled":true,"embedding_ok":true}`,
			wantStatus: "warning",
			wantIn:     []string{"db_ok", "non-boolean"},
			wantNotIn:  []string{"ping failed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := healthVerdict(decodeHealth(t, tc.body))
			if got.Name != "config" {
				t.Errorf("Name = %q, want \"config\"", got.Name)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q (message: %s)", got.Status, tc.wantStatus, got.Message)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(got.Message, want) {
					t.Errorf("message %q does not mention %q", got.Message, want)
				}
			}
			for _, no := range tc.wantNotIn {
				if strings.Contains(got.Message, no) {
					t.Errorf("message %q should not mention %q", got.Message, no)
				}
			}
		})
	}
}

// RunDoctor turns any "error" into a non-zero exit, so a degraded dependency
// must be a warning and not an error: aihub is still serving, and a doctor that
// fails the run because an OPTIONAL backend is down is the CLI-side version of
// the 503 that /v1/health deliberately does not return (aihub#316).
func TestHealthVerdictDegradedIsWarningNotError(t *testing.T) {
	got := healthVerdict(decodeHealth(t,
		`{"status":"degraded","version":"1.2.3","db_ok":false,"embedding_enabled":true,"embedding_ok":false,"embedding_error_kind":"unreachable"}`))
	if got.Status != "warning" {
		t.Fatalf("Status = %q, want \"warning\"", got.Status)
	}
	// Both failing dependencies must be named; "something is wrong" is what the
	// check already said by existing.
	for _, want := range []string{"database", "embedding", "unreachable"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message %q does not mention %q", got.Message, want)
		}
	}
}
