package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// SeedSkill implements skillregistry.SeedStore over the authenticated registry
// API. The registry has no unauthenticated bootstrap path: this method sees
// exactly the identities and versions visible to c's credential.
//
// The lookup is OWNER-SCOPED to the authenticated caller's own namespace
// (aihub#708 final Astra blocker 3): the caller's user id is resolved from
// GET /v1/users/me and sent as the list endpoint's `owner` filter, so the
// name match can only ever select a skill the CALLER owns. The unfiltered
// list is visible-wider than it is writable: another owner's same-name
// skill whose latest version is public or shared with one of the caller's
// projects appears in it, and name-matching over that list let the seed
// adopt — and, for an unscoped admin, publish into — a stranger's identity.
// Three properties hold now:
//
//   - a WRITER's lookup never chooses another owner's public/shared same-name
//     skill: the owner filter excludes it, so the name answers "free" and the
//     seed creates the caller's own identity;
//   - an ADMIN's lookup is scoped the same way — the server applies the
//     owner filter to admins too, so an admin's seed targets the ADMIN'S OWN
//     namespace, never an implicitly-chosen stranger's. A deliberate
//     cross-owner seed has no surface on this port (skillregistry.SeedStore
//     takes no owner argument): targeting another namespace requires new,
//     explicit API — reported as a server-endpoint limitation, not papered
//     over with an implicit default;
//   - the wire is re-verified: a matched item whose owner_user_id disagrees
//     with the resolved caller fails LOUDLY rather than seeding into it, so a
//     server that ignored or lost the owner filter cannot silently turn the
//     seed into a cross-owner write. There is no fallback to unfiltered
//     listing on any error path — that fallback IS the vulnerability.
//
// The cost is one GET /v1/users/me per call. ApplySeed calls SeedSkill once
// per seed name, so a full seed run pays one extra round trip per skill —
// the honest price of never trusting a name match across namespaces.
func (c *Client) SeedSkill(ctx context.Context, name string) (string, []skillregistry.ExistingVersion, bool, error) {
	me, err := c.WhoAmI(ctx)
	if err != nil {
		return "", nil, false, fmt.Errorf("seed lookup %q: resolve the authenticated caller: %w", name, err)
	}
	ownerID := stringValue(me["user_id"])
	if ownerID == "" {
		return "", nil, false, fmt.Errorf("seed lookup %q: the server did not identify the caller; refusing an unscoped name match", name)
	}
	params := url.Values{"limit": {"200"}, "owner": {ownerID}}
	for {
		page, err := c.ListSkills(ctx, params)
		if err != nil {
			return "", nil, false, err
		}
		items, _ := page["items"].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if stringValue(item["name"]) != name {
				continue
			}
			id := stringValue(item["id"])
			if id == "" {
				return "", nil, false, fmt.Errorf("skill %q response has no id", name)
			}
			// Defense in depth: the owner filter is the server's to apply, so a
			// match that escaped it is a server-side regression — refuse loudly
			// rather than let the seed adopt (or, for an admin, publish into) a
			// namespace the caller never named.
			if got := stringValue(item["owner_user_id"]); got != ownerID {
				return "", nil, false, fmt.Errorf(
					"seed lookup %q matched skill %s owned by %q, not the authenticated caller %q; refusing to cross owner namespaces",
					name, id, got, ownerID)
			}
			listed, err := c.ListSkillVersions(ctx, id)
			if err != nil {
				return "", nil, false, err
			}
			versionItems, _ := listed["items"].([]any)
			versions := make([]skillregistry.ExistingVersion, 0, len(versionItems))
			for _, vr := range versionItems {
				v, _ := vr.(map[string]any)
				version, err := positiveJSONInt(v["version"])
				if err != nil {
					return "", nil, false, fmt.Errorf("skill %q version response: %w", name, err)
				}
				digest := stringValue(v["digest"])
				if digest == "" {
					return "", nil, false, fmt.Errorf("skill %q version %d response has no digest", name, version)
				}
				versions = append(versions, skillregistry.ExistingVersion{Version: version, Digest: digest})
			}
			return id, versions, true, nil
		}
		cursor := stringValue(page["next_cursor"])
		if cursor == "" {
			return "", nil, false, nil
		}
		params.Set("cursor", cursor)
	}
}

// CreateSeedSkill creates a private identity owned by the authenticated caller.
func (c *Client) CreateSeedSkill(ctx context.Context, name string) (string, error) {
	out, err := c.CreateSkill(ctx, map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	id := stringValue(out["id"])
	if id == "" {
		return "", fmt.Errorf("create skill %q response has no id", name)
	}
	return id, nil
}

// PublishSeedSkillVersion publishes immutable private content with the exact
// expected-latest CAS token computed by skillregistry.ApplySeed/ApplyImport.
func (c *Client) PublishSeedSkillVersion(ctx context.Context, skillID string, expectedLatest int, bundle, contract []byte, contentDigest string) (int, error) {
	out, err := c.PublishSkillVersion(ctx, skillID, map[string]any{
		"expected_latest": expectedLatest,
		"bundle":          json.RawMessage(bundle),
		"contract":        json.RawMessage(contract),
		"content_digest":  contentDigest,
	})
	if err != nil {
		return 0, err
	}
	version, err := positiveJSONInt(out["version"])
	if err != nil {
		return 0, fmt.Errorf("publish skill %s response: %w", skillID, err)
	}
	visibility := stringValue(out["visibility"])
	if visibility != "private" {
		return 0, fmt.Errorf("publish skill %s returned visibility %q, want private", skillID, visibility)
	}
	return version, nil
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func positiveJSONInt(v any) (int, error) {
	var n int64
	switch x := v.(type) {
	case float64:
		n = int64(x)
		if float64(n) != x {
			return 0, fmt.Errorf("version %v is not an integer", x)
		}
	case json.Number:
		parsed, err := strconv.ParseInt(x.String(), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid version %q", x)
		}
		n = parsed
	case int:
		n = int64(x)
	case int64:
		n = x
	default:
		return 0, fmt.Errorf("version has type %T", v)
	}
	if n < 1 || int64(int(n)) != n {
		return 0, fmt.Errorf("version %d is not a positive int", n)
	}
	return int(n), nil
}

var _ skillregistry.SeedStore = (*Client)(nil)
