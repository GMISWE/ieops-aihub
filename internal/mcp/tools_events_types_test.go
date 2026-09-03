package mcp_test

// aihub#259 — pf_read_events published a `types` filter it never sent.
//
// # Which layer dropped it
//
// Measured, one layer at a time, because "the filter does not work" has three
// possible homes and the fix belongs in exactly one of them:
//
//	internal/domain/memory.go  ListEvents      builds `event_type IN (...)`   WORKS
//	internal/server/routes_memory.go handleListEvents reads the query param,
//	                                           splits it on commas            WORKS
//	internal/mcp/tools_events.go pf_read_events publishes `types` in its
//	                                           InputSchema and never copies it
//	                                           into url.Values                DROPPED
//
// So the server was never asked to filter. That is why a type which cannot
// exist returned the full event stream rather than nothing, and why the same
// call over REST — reported separately — did filter: the REST caller sets the
// query parameter itself and never touches the hop that was broken.
//
// TestReadEventsWireQuery below pins the hop that was actually broken.
// TestE2EReadEventsTypesFilterDiscriminates pins all three at once, so a
// regression in any of them is caught by something.
//
// # Why "returns some events" is not an acceptable assertion
//
// With the defect present the call still returned a non-empty, well-formed,
// correctly-shaped list. Every arm below therefore compares TWO states that the
// defect makes identical — a real type against a type that cannot exist — and
// asserts they differ. An assertion that merely checked for a usable response
// was green throughout the entire life of this bug.
//
// Run:
//
//	go test ./internal/mcp/ -run TestReadEventsWireQuery -v
//	AIHUB_TEST_DB=... go test ./internal/mcp/ -run TestE2EReadEventsTypesFilterDiscriminates -v

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/internal/server"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// readEventsWireProbes is pf_read_events' published contract stated as VALUES on
// the query string, one entry per property the tool advertises.
//
// A key-set comparison would not have caught aihub#259: `types` was present in
// the schema, absent from the wire, and no name check relates those two facts.
// The completeness test below reads the property list back from ListTools — what
// a model actually receives — and fails if any published property has no entry
// here, so adding a parameter without forwarding it cannot be silent again for
// this tool.
var readEventsWireProbes = map[string]struct {
	shape any    // what the caller sends
	want  string // what must appear on the query string
}{
	"work_item_id": {shape: "wi_probe259", want: "wi_probe259"},
	"project":      {shape: "p_probe259", want: "p_probe259"},
	"user_id":      {shape: "u_probe259", want: "u_probe259"},
	// The defect. An array of two renders comma-separated, which is the form
	// handleListEvents parses with strings.Split.
	"types": {shape: []any{"wi_cancelled", "note"}, want: "wi_cancelled,note"},
	"since": {shape: "2026-08-24T00:00:00Z", want: "2026-08-24T00:00:00Z"},
	// Discriminating value: handleListEvents defaults Limit to 50, so 7 tells
	// "forwarded" apart from "fell back". "50" would be green either way.
	"limit":        {shape: "7", want: "7"},
	"pinned_first": {shape: true, want: "true"},
}

// TestReadEventsWireQueryCarriesEveryPublishedProperty is the hop that was
// broken, asserted on the bytes leaving this process.
func TestReadEventsWireQueryCarriesEveryPublishedProperty(t *testing.T) {
	names := make([]string, 0, len(readEventsWireProbes))
	for name := range readEventsWireProbes {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		probe := readEventsWireProbes[name]
		t.Run(name, func(t *testing.T) {
			q := newQueryRecorder(t)
			// work_item_id is always present: the handler rejects a call that
			// names neither it nor project before any request is made.
			args := map[string]any{"work_item_id": "wi_probe259"}
			args[name] = probe.shape
			callToolAgainstRecorder(t, q, "pf_read_events", args)

			if got := q.last(t); got.Get(name) != probe.want {
				t.Fatalf("pf_read_events(%s=%#v) put %s=%q on the wire, want %q. The property is "+
					"published in the tool's InputSchema, so a caller has every reason to expect it "+
					"to be applied; dropping it here is aihub#259's exact shape. Full query: %v",
					name, probe.shape, name, got.Get(name), probe.want, got)
			}
		})
	}
}

// TestReadEventsWireQueryAcceptsAScalarType is the aihub#280 lesson applied to
// this parameter: a caller that sends the scalar form of an array-typed property
// must not be silently dropped either. Six polyforge skills had done exactly that
// with `ids` and `status`.
func TestReadEventsWireQueryAcceptsAScalarType(t *testing.T) {
	q := newQueryRecorder(t)
	callToolAgainstRecorder(t, q, "pf_read_events", map[string]any{
		"work_item_id": "wi_probe259", "types": "wi_cancelled",
	})
	if got := q.last(t); got.Get("types") != "wi_cancelled" {
		t.Fatalf("pf_read_events(types=%q) put types=%q on the wire; a bare string must be accepted "+
			"as a one-element selection, not discarded. Full query: %v", "wi_cancelled", got.Get("types"), got)
	}
}

// TestReadEventsWireQueryOmitsTypesWhenUnset keeps the fix from overshooting:
// "no types given" must remain "no filter", never an empty selection that
// matches nothing. Without this, forwarding an empty string would turn every
// unfiltered read into a silent zero-result read.
func TestReadEventsWireQueryOmitsTypesWhenUnset(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{"absent", nil},
		{"an empty array", map[string]any{"types": []any{}}},
		{"an array of empty strings", map[string]any{"types": []any{"", ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQueryRecorder(t)
			args := map[string]any{"work_item_id": "wi_probe259"}
			for k, v := range tc.extra {
				args[k] = v
			}
			callToolAgainstRecorder(t, q, "pf_read_events", args)
			if got := q.last(t); got.Has("types") {
				t.Fatalf("types=%q was sent for the %s case; an unusable selection must mean "+
					"'unfiltered', or every plain read silently returns nothing: %v",
					got.Get("types"), tc.name, got)
			}
		})
	}
}

// TestReadEventsEveryPublishedPropertyHasAWireProbe is the completeness half,
// read back from ListTools rather than from the schema literal — a schema
// function can be perfect and still not be the one registered.
//
// ⚠️ Scope, stated so it is not overread: this closes the "published but never
// forwarded" class for pf_read_events only. aihub#148 closed it for pf_recall,
// aihub#325 for the tools_memory.go tools, and this is the third recurrence of
// the same class in a third file. Nothing here guards the other MCP tool files.
func TestReadEventsEveryPublishedPropertyHasAWireProbe(t *testing.T) {
	q := newQueryRecorder(t)
	server := mcp.New(nil, client.New(q.server.URL, "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		s, err := server.Connect(ctx, sTransport)
		if err != nil {
			return
		}
		_ = s.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "events-schema", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var props map[string]any
	for _, tool := range res.Tools {
		if tool.Name != "pf_read_events" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("re-marshal schema: %v", err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("schema is not valid JSON: %v", err)
		}
		props = schema.Properties
	}
	if len(props) == 0 {
		t.Fatal("pf_read_events publishes no properties, or ListTools did not return it; " +
			"every check in this file would pass vacuously")
	}
	for name := range props {
		if _, ok := readEventsWireProbes[name]; !ok {
			t.Errorf("pf_read_events publishes %q with no wire probe — a published parameter with "+
				"no by-value assertion is one that may never reach the server, which is exactly "+
				"how `types` went unnoticed", name)
		}
	}
	for name := range readEventsWireProbes {
		if _, ok := props[name]; !ok {
			t.Errorf("there is a wire probe for %q, which pf_read_events no longer publishes — stale probe", name)
		}
	}
}

// ─── end to end, through the real router and a real database ─────────────────

// eventsE2EStack is the whole chain: migrated DB, real echo router, real
// pkg/client, real MCP server — so an assertion covers the MCP hop, the handler
// and the SQL together.
type eventsE2EStack struct {
	pool    *pgxpool.Pool
	session *sdkmcp.ClientSession
	wiID    string
}

func newEventsE2EStack(t *testing.T) *eventsE2EStack {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to AIHUB_TEST_DB: %v", err)
	}
	t.Cleanup(pool.Close)

	const (
		uid     = "u_events259"
		rawKey  = "pfk_events259_test_key"
		project = "p_events259"
		wiID    = "wi_events259"
	)

	keys, err := json.Marshal([]map[string]any{{"id": "k_ev259", "key_hash": auth.HashKey(rawKey)}})
	if err != nil {
		t.Fatalf("marshal api keys: %v", err)
	}
	mustSeed := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed (%s): %v", q, err)
		}
	}
	mustSeed(`INSERT INTO users(id,email,display_name,user_type,role,api_keys)
		VALUES($1,$1||'@test.local',$1,'human','admin',$2)
		ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role='admin'`, uid, keys)
	mustSeed(`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`, project, uid)
	mustSeed(`DELETE FROM agent_events WHERE work_item_id=$1`, wiID)
	mustSeed(`DELETE FROM work_items WHERE id=$1`, wiID)
	// seq is assigned by CreateWorkItem rather than by a column default, so a raw
	// insert has to supply it; slug is a GENERATED column and must NOT be
	// supplied. Raw rather than CreateWorkItem on purpose: this fixture is about
	// the events attached to a work item, and going through the create path would
	// drag in goal-similarity dedup and an embedding refresh.
	mustSeed(`INSERT INTO work_items(id,seq,project,scenario,goal,source,status,reporter_user_id,reporter_display,wi_type,requires_human_session)
		VALUES($1,259259,$2,'coding','events filter fixture','human','queued',$3,$3,'fix_bug',false)`,
		wiID, project, uid)

	// THREE populations of unequal size — 3 notes, 2 cancellations, 1 reclassify
	// — chosen so that every count a test can assert is distinct: 2, 3, 5 (the
	// two-type union) and 6 (everything).
	//
	// The third type is what makes the union arm discriminating. With only two
	// types seeded, "filter on both" and "do not filter at all" both return the
	// whole corpus, so that arm would stay green with the parameter dropped —
	// measured, not assumed: it was the one arm that passed against the mutant
	// before this row was added.
	for i := 0; i < 3; i++ {
		mustSeed(`INSERT INTO agent_events(id,work_item_id,event_type,payload,project)
			VALUES($1,$2,'note','{}'::jsonb,$3)`, "evt_note259_"+string(rune('a'+i)), wiID, project)
	}
	for i := 0; i < 2; i++ {
		mustSeed(`INSERT INTO agent_events(id,work_item_id,event_type,payload,project)
			VALUES($1,$2,'wi_cancelled','{}'::jsonb,$3)`, "evt_canc259_"+string(rune('a'+i)), wiID, project)
	}
	mustSeed(`INSERT INTO agent_events(id,work_item_id,event_type,payload,project)
		VALUES($1,$2,'wi_reclassified','{}'::jsonb,$3)`, "evt_recl259_a", wiID, project)

	ts := httptest.NewServer(server.NewRouter(pool, []byte("events259-cookie-secret-not-a-real-one")))
	t.Cleanup(ts.Close)

	mcpServer := mcp.New(nil, client.New(ts.URL, rawKey))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		s, err := mcpServer.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = s.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "events-e2e", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return &eventsE2EStack{pool: pool, session: session, wiID: wiID}
}

// readEventIDs calls pf_read_events through the real stack and returns the ids
// and the types it got back.
func (s *eventsE2EStack) readEventIDs(t *testing.T, types any) (ids []string, gotTypes []string) {
	t.Helper()
	args := map[string]any{"work_item_id": s.wiID, "limit": "50"}
	if types != nil {
		args["types"] = types
	}
	res, err := s.session.CallTool(context.Background(),
		&sdkmcp.CallToolParams{Name: "pf_read_events", Arguments: args})
	if err != nil {
		t.Fatalf("call pf_read_events(types=%v): %v", types, err)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("pf_read_events returned %T, want TextContent", res.Content[0])
	}
	if res.IsError {
		t.Fatalf("pf_read_events(types=%v) failed: %s", types, text.Text)
	}
	var decoded struct {
		Events []struct {
			ID        string `json:"id"`
			EventType string `json:"event_type"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("pf_read_events output is not JSON: %v (%q)", err, text.Text)
	}
	for _, e := range decoded.Events {
		ids = append(ids, e.ID)
		gotTypes = append(gotTypes, e.EventType)
	}
	sort.Strings(ids)
	return ids, gotTypes
}

// TestE2EReadEventsTypesFilterDiscriminates is aihub#259's acceptance criterion,
// written the way the item specifies: a real type and a type that cannot exist
// must not return the same events.
//
// One function with subtests: dbtestcov counts DB-gated FUNCTIONS and ci.yml
// asserts the per-arm claims by subtest name.
func TestE2EReadEventsTypesFilterDiscriminates(t *testing.T) {
	s := newEventsE2EStack(t)

	unfiltered, _ := s.readEventIDs(t, nil)
	if len(unfiltered) != 6 {
		t.Fatalf("fixture check: the unfiltered read returned %d events, want the 6 seeded; "+
			"nothing below measures filtering if the corpus is wrong: %v", len(unfiltered), unfiltered)
	}

	// MUTANT: internal/mcp/tools_events.go — delete the
	// setIfNonempty(params, "types", ...) line. Every arm below goes red;
	// "an unfiltered read still returns everything" stays green, which is what
	// separates "the filter works" from "the endpoint broke".
	t.Run("a real type returns only that type", func(t *testing.T) {
		ids, types := s.readEventIDs(t, []any{"wi_cancelled"})
		if len(ids) != 2 {
			t.Errorf("filtering on wi_cancelled returned %d events, want the 2 seeded with that type; "+
				"got %v (types %v)", len(ids), ids, types)
		}
		for i, ty := range types {
			if ty != "wi_cancelled" {
				t.Errorf("event %s has type %q in a wi_cancelled-filtered read — this is the false "+
					"green the item describes: a caller reads a non-empty result as proof those "+
					"events happened, and they are some other type entirely", ids[i], ty)
			}
		}
	})

	t.Run("a type that cannot exist returns nothing", func(t *testing.T) {
		ids, types := s.readEventIDs(t, []any{"zzz_definitely_not_a_real_event_type"})
		if len(ids) != 0 {
			t.Errorf("a type that cannot exist returned %d events (%v, types %v). With the defect "+
				"present this returned the FULL stream, which is how 'I filtered and saw no cancel "+
				"events' became a false all-clear over 44 real cancellations (ieops#680)",
				len(ids), ids, types)
		}
	})

	// The item's own wording: the two id sets must DIFFER. Stated separately
	// from the counts because it is the comparison that has two distinct states
	// while the defect is present — with it, these two sets were identical.
	t.Run("the real and impossible type return different id sets", func(t *testing.T) {
		real, _ := s.readEventIDs(t, []any{"wi_cancelled"})
		impossible, _ := s.readEventIDs(t, []any{"zzz_definitely_not_a_real_event_type"})
		if len(real) == len(impossible) && equalIDs(real, impossible) {
			t.Errorf("a real type and an impossible one returned identical events (%v). That "+
				"equality IS the defect: it rules out 'the filter matched nothing', because a "+
				"filter that ran could not give the same answer for both", real)
		}
	})

	// More than one type, which is the only arm that exercises the comma
	// rendering between csvArg and handleListEvents' strings.Split.
	t.Run("two types return their union", func(t *testing.T) {
		ids, _ := s.readEventIDs(t, []any{"wi_cancelled", "note"})
		if len(ids) != 5 {
			t.Errorf("filtering on wi_cancelled+note returned %d events, want the 5 of those two "+
				"types out of 6 seeded; a broken separator would return only the first type's "+
				"events or none, and a dropped parameter would return all 6: %v", len(ids), ids)
		}
	})

	// The control: filtering must not be achieved by breaking the endpoint.
	t.Run("an unfiltered read still returns everything", func(t *testing.T) {
		ids, _ := s.readEventIDs(t, nil)
		if len(ids) != 6 {
			t.Errorf("the unfiltered read returned %d events, want all 6 — the fix must not turn "+
				"'no types given' into an empty selection: %v", len(ids), ids)
		}
	})
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
