package server

// aihub#530 — PATCH /v1/admin/users/:id must REFUSE `user_type`, not silently
// drop it. Until this change the request struct did not name the field, so
// c.Bind discarded it: sent alone it fell into 400 "no fields to update" (a
// message about a body the caller did not send), and sent beside a rename the
// caller got {"ok":true} for an identity write that never happened.
//
// Refusal rather than binding, decided on two invariants the handler cannot
// hold if the field is writable here:
//   - user_type is identity (human vs machine), and the create-time email
//     invariant hangs off it — handleCreateUser generates a machine user's
//     mailbox and requires a human's, while THIS handler binds no email, so a
//     flipped user_type strands the row on the wrong side of that pairing;
//   - pf_update_user deliberately does not publish the field (M40 in
//     user_admin_write_shape_test.go: publishing it reds K3), so binding it
//     would be the aihub#419 BOUND_FIELD_UNPUBLISHED shape.
//
// The nil pool is the instrument, as in update_user_vocab_test.go: any DB
// access panics, so "was refused before the UPDATE" and "reached the UPDATE"
// are distinguishable with no database.
//
//	GOWORK=off go test ./internal/server/ -run TestUpdateUserUserType -count=1 -v
//
// MUTANTS (aihub#530, 2026-09-11 — each applied to this tree, run RED,
// restored from a cp backup):
//
//	U1 enforcement: remove the `req.UserType != nil` refusal AND the struct
//	    field (the pre-aihub#530 handler)     RED  beside_a_rename_is_refused_whole
//	                                               (the body reaches the UPDATE)
//	                                          RED  alone_names_the_field (the 400
//	                                               is "no fields to update", no
//	                                               details.field)
//	U2 enforcement: keep the binding, replace the refusal with a SET clause
//	                                          RED  both refusal legs here and
//	                                               user_type_is_not_a_write in
//	                                               user_admin_write_shape_test.go
//	U3 over-refusal: bind json.RawMessage instead of *string, so the explicit
//	    null spelling also trips the refusal  RED  null_user_type_is_absent (only)
//	U4 control: refuse every request          RED  the_two_updatable_fields_still_write
//	                                               and null_user_type_is_absent

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestUpdateUserUserTypeIsRefusedNotDropped holds the aihub#530 verdict leg by
// leg. The refusal legs assert on the structured `details`, not on a message
// substring — details.field is what a caller can parse, and it is also the
// property the silent-drop handler could not produce.
func TestUpdateUserUserTypeIsRefusedNotDropped(t *testing.T) {
	decode := func(t *testing.T, raw []byte) (field, got string) {
		t.Helper()
		var body struct {
			Details struct {
				Field string `json:"field"`
				Got   string `json:"got"`
			} `json:"details"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode error body %s: %v", raw, err)
		}
		return body.Details.Field, body.Details.Got
	}

	// The discriminating leg: on the silent-drop handler this body WRITES —
	// the rename lands, user_type vanishes, and the caller is told ok. The
	// verdict must be whole-request, because the half the caller most likely
	// cared about is the half that would be dropped.
	t.Run("beside_a_rename_is_refused_whole", func(t *testing.T) {
		rec := new(recorderHolder)
		reached := reachesTheStatement(func() {
			rec.r = updateUserRequest(t, nil, map[string]any{
				"display_name": "Probe 530", "user_type": "machine",
			})
		})
		if reached {
			t.Fatal("a body carrying user_type beside a rename reached the UPDATE — the rename " +
				"half-succeeded and the identity half was silently dropped, which is exactly " +
				"the pre-aihub#530 behaviour")
		}
		if rec.r.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (body: %s)", rec.r.Code, rec.r.Body.String())
		}
		field, got := decode(t, rec.r.Body.Bytes())
		if field != "user_type" {
			t.Errorf("details.field = %q, want \"user_type\" — the refusal does not name the "+
				"field it refused (body: %s)", field, rec.r.Body.String())
		}
		if got != "machine" {
			t.Errorf("details.got = %q, want \"machine\" — the answer does not echo the "+
				"rejected value (body: %s)", got, rec.r.Body.String())
		}
	})

	// Sent alone, the old handler answered 400 "no fields to update" — a true
	// verdict with a false diagnosis, since the caller DID send a field. The
	// refusal must name user_type, not describe an empty body.
	t.Run("alone_names_the_field", func(t *testing.T) {
		rec := new(recorderHolder)
		reached := reachesTheStatement(func() {
			rec.r = updateUserRequest(t, nil, map[string]any{"user_type": "human"})
		})
		if reached {
			t.Fatal("a body carrying only user_type reached the UPDATE")
		}
		if rec.r.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (body: %s)", rec.r.Code, rec.r.Body.String())
		}
		if field, _ := decode(t, rec.r.Body.Bytes()); field != "user_type" {
			t.Errorf("details.field = %q, want \"user_type\" — sent alone, the field must be "+
				"named rather than reported as \"no fields to update\" (body: %s)",
				field, rec.r.Body.String())
		}
	})

	// JSON null binds a *string to nil, so it counts as absent — the same
	// verdict display_name and role give it. A refusal keyed on the raw body
	// rather than on the decoded pointer would red here.
	t.Run("null_user_type_is_absent", func(t *testing.T) {
		reached := reachesTheStatement(func() {
			_ = updateUserRequest(t, nil, map[string]any{
				"display_name": "Probe 530 null", "user_type": nil,
			})
		})
		if !reached {
			t.Error("a rename with user_type:null was refused before the UPDATE — null decodes " +
				"to a nil pointer and must count as absent, as it does for the other two fields")
		}
	})

	// The control for "the handler still updates anything at all": both
	// updatable fields together must reach the UPDATE, or every refusal above
	// is green because nothing gets through.
	t.Run("the_two_updatable_fields_still_write", func(t *testing.T) {
		reached := reachesTheStatement(func() {
			_ = updateUserRequest(t, nil, map[string]any{
				"display_name": "Probe 530 control", "role": "admin",
			})
		})
		if !reached {
			t.Error("a body carrying only the two updatable fields was refused before the UPDATE")
		}
	})
}
