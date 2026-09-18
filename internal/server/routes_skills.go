package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// A 2 MiB canonical bundle can expand when escaped in JSON; cap the entire
// envelope at 16 MiB (contract and transport overhead remain bounded).
const skillRequestBodyLimit = 16 << 20

// RegisterSkillRoutes exposes the authenticated, versioned skill registry.
func RegisterSkillRoutes(v1 *echo.Group, pool *pgxpool.Pool) {
	v1.POST("/skills", handleCreateSkill(pool))
	v1.GET("/skills", handleListSkills(pool))
	v1.GET("/skills/:id", handleGetSkill(pool))
	v1.POST("/skills/:id/versions", handlePublishSkillVersion(pool))
	v1.GET("/skills/:id/versions", handleListSkillVersions(pool))
	v1.GET("/skills/:id/versions/:version", handleGetSkillVersion(pool))
	v1.POST("/skills/:id/versions/:version/shares", handleShareSkillVersion(pool))
	v1.DELETE("/skills/:id/versions/:version/shares/:project", handleRevokeSkillVersion(pool))
	v1.PATCH("/skills/:id/versions/:version/visibility", handleSetSkillVisibility(pool))
}

// skillJSONDecode applies the registry's bounded, closed request-body contract.
// Domain validation remains authoritative for field values.
func skillJSONDecode(c echo.Context, dst any) *domain.AihubError {
	r := http.MaxBytesReader(c.Response(), c.Request().Body, skillRequestBodyLimit)
	// Read once with a hard cap; validation must not normalize raw bundle/contract JSON.
	raw, err := io.ReadAll(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return domain.NewErr(domain.ErrBadRequest, fmt.Sprintf("request body exceeds %d bytes", skillRequestBodyLimit))
		}
		return domain.NewErr(domain.ErrBadRequest, "invalid request body")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := checkSkillJSONObject(dec, reflect.TypeOf(dst).Elem(), true); err != nil {
		return domain.NewErr(domain.ErrBadRequest, "invalid request body")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return domain.NewErr(domain.ErrBadRequest, "invalid request body")
	}
	return nil
}

// Walk tokens before decoding: encoding/json otherwise accepts duplicate names and
// case-insensitive struct aliases. Only the outer request uses a closed tag set;
// nested bundle/contract objects have domain-owned, potentially arbitrary keys.
func checkSkillJSONObject(dec *json.Decoder, shape reflect.Type, root bool) error {
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return fmt.Errorf("expected object")
	}
	allowed := map[string]bool{}
	if root {
		for i := 0; i < shape.NumField(); i++ {
			name := strings.Split(shape.Field(i).Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = true
			}
		}
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] || (root && !allowed[name]) {
			return fmt.Errorf("duplicate or unknown field")
		}
		seen[name] = true
		if err := checkSkillJSONValue(dec); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if root {
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return fmt.Errorf("trailing JSON")
		}
	}
	return nil
}

func checkSkillJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate field")
			}
			seen[name] = true
			if err := checkSkillJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for dec.More() {
			if err := checkSkillJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	return nil
}

func skillVersionParam(c echo.Context) (int, *domain.AihubError) {
	v, err := strconv.Atoi(c.Param("version"))
	if err != nil || v < 1 {
		return 0, domain.NewErr(domain.ErrBadRequest, "version must be a positive integer")
	}
	return v, nil
}

func skillCaller(c echo.Context) (*domain.UserRecord, *domain.AihubError) {
	u := GetUser(c)
	if u == nil {
		return nil, domain.NewErr(domain.ErrUnauthorized, "not authenticated")
	}
	return callerToUserRecord(u), nil
}

func handleCreateSkill(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		var req domain.CreateSkillRequest
		if aerr = skillJSONDecode(c, &req); aerr != nil {
			return writeError(c, aerr)
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		out, aerr := domain.CreateSkill(ctx, pool, caller, req)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusCreated, out)
	}
}

func handleListSkills(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		for key := range c.QueryParams() {
			switch key {
			case "owner", "limit", "cursor":
			default:
				return writeError(c, domain.NewErr(domain.ErrBadRequest, fmt.Sprintf("unknown query parameter %q", key)))
			}
		}
		req := domain.ListSkillsRequest{}
		cursor, _, cursorErr := queryCursor(c, "cursor", domain.RecallCursorTimestamp)
		if cursorErr != nil {
			return writeError(c, cursorErr)
		}
		if cursor != "" {
			_, req.Cursor, _ = strings.Cut(cursor, "|")
			if req.Cursor == "" {
				return writeError(c, domain.NewErr(domain.ErrBadRequest, "cursor must be a page token from a previous response's next_cursor"))
			}
		}
		if owner := c.QueryParam("owner"); owner != "" {
			req.Owner = &owner
		}
		if limit, present, limitErr := queryInt(c, "limit"); limitErr != nil {
			return writeError(c, limitErr)
		} else if present {
			req.Limit = limit
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		out, aerr := domain.ListSkills(ctx, pool, caller, req)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		response := map[string]any{"items": out}
		effectiveLimit := req.Limit
		if effectiveLimit == 0 {
			effectiveLimit = 50
		}
		if len(out) == effectiveLimit && len(out) > 0 {
			last := out[len(out)-1]
			response["next_cursor"] = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
		}
		return c.JSON(http.StatusOK, response)
	}
}

func handleGetSkill(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		out, aerr := domain.GetSkill(ctx, pool, caller, c.Param("id"))
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}

func handlePublishSkillVersion(pool *pgxpool.Pool) echo.HandlerFunc {
	type publishBody struct {
		ExpectedLatest *int            `json:"expected_latest"`
		Bundle         json.RawMessage `json:"bundle"`
		Contract       json.RawMessage `json:"contract"`
		ContentDigest  string          `json:"content_digest,omitempty"`
	}
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		var body publishBody
		if aerr = skillJSONDecode(c, &body); aerr != nil {
			return writeError(c, aerr)
		}
		if body.ExpectedLatest == nil {
			return writeError(c, domain.NewErr(domain.ErrBadRequest, "expected_latest is required"))
		}
		req := domain.PublishSkillVersionRequest{
			SkillID:        c.Param("id"),
			ExpectedLatest: *body.ExpectedLatest,
			Bundle:         body.Bundle,
			Contract:       body.Contract,
			ContentDigest:  body.ContentDigest,
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		out, aerr := domain.PublishSkillVersion(ctx, pool, caller, req)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusCreated, out)
	}
}

func handleListSkillVersions(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		out, aerr := domain.ListSkillVersions(ctx, pool, caller, c.Param("id"))
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, map[string]any{"items": out})
	}
}

func handleGetSkillVersion(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		version, aerr := skillVersionParam(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		out, aerr := domain.GetSkillVersion(ctx, pool, caller, c.Param("id"), version)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}

func handleShareSkillVersion(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		version, aerr := skillVersionParam(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		var body struct {
			Project string `json:"project"`
		}
		if aerr = skillJSONDecode(c, &body); aerr != nil {
			return writeError(c, aerr)
		}
		req := domain.SkillVersionShareRequest{SkillID: c.Param("id"), Version: version, Project: body.Project}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		if aerr = domain.ShareSkillVersionWithProject(ctx, pool, caller, req); aerr != nil {
			return domainErr(c, aerr)
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func handleRevokeSkillVersion(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		version, aerr := skillVersionParam(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		req := domain.SkillVersionShareRequest{SkillID: c.Param("id"), Version: version, Project: c.Param("project")}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		if aerr = domain.RevokeSkillVersionFromProject(ctx, pool, caller, req); aerr != nil {
			return domainErr(c, aerr)
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func handleSetSkillVisibility(pool *pgxpool.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		caller, aerr := skillCaller(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		version, aerr := skillVersionParam(c)
		if aerr != nil {
			return writeError(c, aerr)
		}
		var body struct {
			Visibility string `json:"visibility"`
		}
		if aerr = skillJSONDecode(c, &body); aerr != nil {
			return writeError(c, aerr)
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		out, aerr := domain.SetSkillVersionVisibility(ctx, pool, caller, c.Param("id"), version, body.Visibility)
		if aerr != nil {
			return domainErr(c, aerr)
		}
		return c.JSON(http.StatusOK, out)
	}
}
