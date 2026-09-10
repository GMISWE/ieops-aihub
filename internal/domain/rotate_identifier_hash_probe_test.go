package domain

import (
	"strings"
	"testing"
)

// aihub#591 — pf_rotate_identifier's hop-4 lead, pinned: "Generates a new
// identifier, stores its bcrypt hash, and returns the plaintext once."
//
// Nothing held it: the only arm near this tool is the ownership-ladder one, and
// the hash discipline — the property that makes "once" true, because a stored
// hash cannot be re-disclosed — was carried by a code comment ("plain never
// stored") that no test read. Same instrument as the isolation-level probe: the
// comment-stripped source, because the claim is about WHAT THE WRITE STORES and
// the UPDATE statement is that fact's primary form.
func TestRotateIdentifierStoresABcryptHashAndReturnsThePlain(t *testing.T) {
	code := stripComments(t, sourceOf(t, "projects.go"))
	body := bodyOf(t, code, "RotateIdentifier")

	if !strings.Contains(body, "bcrypt.GenerateFromPassword([]byte(plain), 12)") {
		t.Errorf("RotateIdentifier no longer bcrypt-hashes the plain identifier at cost 12. " +
			"The card publishes \"stores its bcrypt hash\"; if the algorithm or cost moved, " +
			"move the card sentence in the same change — and if the hash is GONE, the " +
			"identifier is being stored in a re-disclosable form, which breaks the \"once\".")
	}
	if !strings.Contains(body, "UPDATE projects SET identifier_hash=$1, identifier_prefix=$2 WHERE name=$3") {
		t.Errorf("RotateIdentifier's UPDATE no longer writes exactly identifier_hash + " +
			"identifier_prefix. The card's \"once\" rests on the row holding only the hash " +
			"and the prefix — a third stored column here is where a plaintext leak would " +
			"start, so this arm asks you to re-read the card sentence before widening it.")
	}
	if !strings.Contains(body, "return plain, prefix, nil") {
		t.Errorf("RotateIdentifier no longer returns the plain identifier to the caller — " +
			"the response is the caller's ONLY sight of it, so the card's \"returns the " +
			"plaintext once\" just went false in its other half.")
	}
}
