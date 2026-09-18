package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// CreateSkill creates a private skill identity. Versions are published
// separately and are private by default.
func (c *Client) CreateSkill(ctx context.Context, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/skills", body, &out)
}

// ListSkills lists skills and latest-accessible version metadata visible to
// the caller. Supported parameters are owner, cursor and limit.
func (c *Client) ListSkills(ctx context.Context, params url.Values) (map[string]any, error) {
	path := "/v1/skills"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out map[string]any
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

// GetSkill returns one skill identity and its latest accessible version.
func (c *Client) GetSkill(ctx context.Context, skillID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodGet, "/v1/skills/"+seg(skillID), nil, &out)
}

// PublishSkillVersion publishes the next immutable version. body must carry
// expected_latest, bundle and contract; content_digest is optional.
func (c *Client) PublishSkillVersion(ctx context.Context, skillID string, body any) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, "/v1/skills/"+seg(skillID)+"/versions", body, &out)
}

// ListSkillVersions lists only versions accessible to the caller.
func (c *Client) ListSkillVersions(ctx context.Context, skillID string) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodGet, "/v1/skills/"+seg(skillID)+"/versions", nil, &out)
}

// GetSkillVersion returns one exact accessible version including its bundle
// and runtime contract.
func (c *Client) GetSkillVersion(ctx context.Context, skillID string, version int) (map[string]any, error) {
	var out map[string]any
	path := "/v1/skills/" + seg(skillID) + "/versions/" + strconv.Itoa(version)
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

// ShareSkillVersion grants one project access to one exact version.
func (c *Client) ShareSkillVersion(ctx context.Context, skillID string, version int, project string) error {
	path := "/v1/skills/" + seg(skillID) + "/versions/" + strconv.Itoa(version) + "/shares"
	return c.do(ctx, http.MethodPost, path, map[string]string{"project": project}, nil)
}

// RevokeSkillVersionShare removes one exact version's project grant.
func (c *Client) RevokeSkillVersionShare(ctx context.Context, skillID string, version int, project string) error {
	path := "/v1/skills/" + seg(skillID) + "/versions/" + strconv.Itoa(version) + "/shares/" + seg(project)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// SetSkillVersionVisibility changes one exact version between private and
// public. Public still requires an authenticated registry caller.
func (c *Client) SetSkillVersionVisibility(ctx context.Context, skillID string, version int, visibility string) (map[string]any, error) {
	path := "/v1/skills/" + seg(skillID) + "/versions/" + strconv.Itoa(version) + "/visibility"
	var out map[string]any
	return out, c.do(ctx, http.MethodPatch, path, map[string]string{"visibility": visibility}, &out)
}
