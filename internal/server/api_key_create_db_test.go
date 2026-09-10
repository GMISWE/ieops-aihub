package server

// aihub#591 — pf_create_api_key's hop-4 sentence, pinned: "Mints a key, stores
// its hash, and returns the plaintext once."
//
// Nothing held any of its three clauses: the handler had no test at all, and the
// card's only arm on this claim (api_key_surface_test.go) reads the PUBLISHED
// DESCRIPTION — it asserts the caller is told "once", not that the server
// behaves that way. This is the description/behaviour split aihub#543 is about,
// on the one tool whose output is a credential.
//
// "Once" is pinned in its enforceable form: the stored record carries the HASH
// and never the plaintext, so no later read can return the key — there is no
// second disclosure for an endpoint to leak. The strongest half is the round
// trip: the minted plaintext must AUTHENTICATE a real request through the real
// middleware, which is what "stores its hash" is for.
//
//	AIHUB_TEST_DB=postgres://... go test ./internal/server/ -run TestCreateAPIKey -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

const apiKeyCreateAdminKey = "pfk_api_key_create_http_test_key"

func TestCreateAPIKeyStoresOnlyTheHashAndTheMintedKeyAuthenticates(t *testing.T) {
	pool := serverTestPool(t)
	ctx := context.Background()

	uid := "u_" + testname.Sanitize(t.Name())
	keys, err := json.Marshal([]map[string]any{{"id": "k_apikeyprobe", "key_hash": auth.HashKey(apiKeyCreateAdminKey)}})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users(id,email,display_name,user_type,role,api_keys)
		VALUES($1,$1||'@test.local',$1,'human','admin',$2)
		ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role='admin'`, uid, keys)
	require.NoError(t, err)

	ts := httptest.NewServer(NewRouter(pool, []byte("api-key-create-test-cookie-secret")))
	t.Cleanup(ts.Close)

	body := strings.NewReader(`{"name":"aihub#591 probe"}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/admin/users/"+uid+"/keys", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+apiKeyCreateAdminKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create answered: %s", raw)

	var minted struct {
		KeyID  string `json:"key_id"`
		RawKey string `json:"raw_key"`
	}
	require.NoError(t, json.Unmarshal(raw, &minted))
	if minted.KeyID == "" || !strings.HasPrefix(minted.RawKey, "pf_k1_") {
		t.Fatalf("the mint did not come back on the response: %s — this is the caller's ONLY "+
			"sight of the plaintext, which is what the description's \"once\" promises", raw)
	}

	t.Run("the_row_stores_the_hash_and_never_the_plaintext", func(t *testing.T) {
		var stored string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT api_keys::text FROM users WHERE id=$1`, uid).Scan(&stored))
		if strings.Contains(stored, minted.RawKey) {
			t.Fatalf("users.api_keys contains the plaintext key. \"Returns the plaintext once\" "+
				"is only true because the stored form is a hash — a stored plaintext is a second "+
				"read waiting for an endpoint to leak it. Row: %s", stored)
		}
		if !strings.Contains(stored, auth.HashKey(minted.RawKey)) {
			t.Errorf("users.api_keys does not contain HashKey(raw_key), so the middleware can "+
				"never match this key and the mint sold the caller a credential that does not "+
				"authenticate. Row: %s", stored)
		}
		if !strings.Contains(stored, fmt.Sprintf("%q", minted.KeyID)) {
			t.Errorf("the returned key_id %q is not in the stored array: %s", minted.KeyID, stored)
		}
	})

	t.Run("the_minted_key_authenticates_a_real_request", func(t *testing.T) {
		r, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/projects", nil)
		require.NoError(t, err)
		r.Header.Set("Authorization", "Bearer "+minted.RawKey)
		got, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		defer got.Body.Close() //nolint:errcheck
		if got.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(got.Body)
			t.Fatalf("a request under the minted key answered %d (%s). The mint stores "+
				"domain.HashSecret and the middleware compares auth.HashKey — if those two "+
				"ever diverge, every key this tool creates is dead on arrival, and this is "+
				"the arm that says so.", got.StatusCode, b)
		}
	})
}
