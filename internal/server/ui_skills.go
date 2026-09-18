package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

type skillListPageData struct {
	Title   string
	Active  string
	Theme   string
	User    *UserContext
	Items   []domain.SkillDetail
	Owner   string
	Cursor  string
	Limit   int
	Next    string
	Message string
	Error   string
}

type skillDetailPageData struct {
	Title     string
	Active    string
	Theme     string
	User      *UserContext
	Skill     *domain.SkillDetail
	Versions  []domain.SkillVersionSummary
	Version   *domain.SkillVersion
	Bundle    string
	Contract  string
	CSRFToken string
	CanManage bool
	Message   string
	Error     string
}

func registerUISkillHandlers(g *echo.Group, pool *pgxpool.Pool, _ *template.Template, sm *SessionManager) {
	listTmpl := pageTemplate("skills.html.tmpl")
	detailTmpl := pageTemplate("skills_detail.html.tmpl")

	g.GET("/skills", handleUISkills(pool, listTmpl))
	g.GET("/skills/:id", handleUISkillDetail(pool, detailTmpl, sm))
	g.GET("/skills/:id/versions/:version", handleUISkillVersion(pool, detailTmpl, sm))
	g.POST("/skills/:id/versions", handleUIPublishSkill(pool, sm))
	g.POST("/skills/:id/versions/:version/shares", handleUIShareSkill(pool, sm, false))
	g.POST("/skills/:id/versions/:version/shares/revoke", handleUIShareSkill(pool, sm, true))
	g.POST("/skills/:id/versions/:version/visibility", handleUISkillVisibility(pool, sm))
}

func handleUISkills(pool *pgxpool.Pool, tmpl *template.Template) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		data := skillListPageData{
			Title: "Skills", Active: "skills", Theme: themeFromCookie(c), User: u,
			Owner: c.QueryParam("owner"), Limit: queryIntLenientUI(c, "limit", 50, 200),
			Message: c.QueryParam("message"), Error: c.QueryParam("error"),
		}
		cursor, _, cursorErr := queryCursor(c, "cursor", domain.RecallCursorTimestamp)
		if cursorErr != nil {
			data.Error = cursorErr.Message
		} else if cursor != "" {
			data.Cursor = cursor
		}
		if data.Error == "" {
			req := domain.ListSkillsRequest{Limit: data.Limit}
			if data.Cursor != "" {
				_, req.Cursor, _ = strings.Cut(data.Cursor, "|")
				if req.Cursor == "" {
					data.Error = "cursor must be a page token from a previous response"
				}
			}
			if data.Error != "" {
				return renderTemplate(c, tmpl, "layout", data)
			}
			if data.Owner != "" {
				req.Owner = &data.Owner
			}
			ctx, cancel := contextWithTimeout(c)
			defer cancel()
			items, aerr := domain.ListSkills(ctx, pool, callerToUserRecord(u), req)
			if aerr != nil {
				data.Error = aerr.Message
			} else {
				data.Items = items
				if len(items) == data.Limit {
					last := items[len(items)-1]
					data.Next = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
				}
			}
		}
		return renderTemplate(c, tmpl, "layout", data)
	}
}

func handleUISkillDetail(pool *pgxpool.Pool, tmpl *template.Template, sm *SessionManager) echo.HandlerFunc {
	return func(c echo.Context) error {
		return renderUISkillDetail(c, pool, tmpl, sm, 0)
	}
}

func handleUISkillVersion(pool *pgxpool.Pool, tmpl *template.Template, sm *SessionManager) echo.HandlerFunc {
	return func(c echo.Context) error {
		version, aerr := skillVersionParam(c)
		if aerr != nil {
			return renderSkillUIError(c, tmpl, http.StatusBadRequest, aerr.Message)
		}
		return renderUISkillDetail(c, pool, tmpl, sm, version)
	}
}

func renderUISkillDetail(c echo.Context, pool *pgxpool.Pool, tmpl *template.Template, sm *SessionManager, selectedVersion int) error {
	u := GetUser(c)
	if u == nil {
		return redirectToLogin(c)
	}
	data := skillDetailPageData{
		Title: "Skill", Active: "skills", Theme: themeFromCookie(c), User: u,
		Message: c.QueryParam("message"), Error: c.QueryParam("error"),
	}
	ctx, cancel := contextWithTimeout(c)
	defer cancel()
	caller := callerToUserRecord(u)
	detail, aerr := domain.GetSkill(ctx, pool, caller, c.Param("id"))
	if aerr != nil {
		status := http.StatusInternalServerError
		if aerr.Code == domain.ErrNotFound {
			status = http.StatusNotFound
		}
		return renderSkillUIError(c, tmpl, status, aerr.Message)
	}
	data.Skill = detail
	data.Title = detail.Name
	data.CanManage = u.Role == "admin" || u.UserID == detail.OwnerUserID
	data.CSRFToken = skillCSRFToken(c, sm)
	versions, aerr := domain.ListSkillVersions(ctx, pool, caller, detail.ID)
	if aerr != nil {
		data.Error = aerr.Message
		return renderTemplate(c, tmpl, "layout", data)
	}
	data.Versions = versions
	if selectedVersion > 0 {
		version, getErr := domain.GetSkillVersion(ctx, pool, caller, detail.ID, selectedVersion)
		if getErr != nil {
			status := http.StatusInternalServerError
			if getErr.Code == domain.ErrNotFound {
				status = http.StatusNotFound
			}
			data.Error = getErr.Message
			return renderHTMLStatus(c, tmpl, "layout", data, status)
		}
		data.Version = version
		data.Bundle = prettySkillJSON(version.Bundle)
		data.Contract = prettySkillJSON(version.Contract)
	}
	return renderTemplate(c, tmpl, "layout", data)
}

func renderSkillUIError(c echo.Context, tmpl *template.Template, status int, message string) error {
	u := GetUser(c)
	data := skillDetailPageData{
		Title: "Skill", Active: "skills", Theme: themeFromCookie(c), User: u, Error: message,
	}
	return renderHTMLStatus(c, tmpl, "layout", data, status)
}

func prettySkillJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

func parseSkillUIForm(c echo.Context, sm *SessionManager) error {
	if err := validateSkillFormOrigin(c); err != nil {
		return err
	}
	// URL-encoding can triple an already escaped JSON payload. Keep a separate
	// bounded form cap so every supported bundle fits the browser POST.
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 3*skillRequestBodyLimit)
	if err := c.Request().ParseForm(); err != nil {
		return fmt.Errorf("invalid or oversized form")
	}
	want := skillCSRFToken(c, sm)
	got := c.Request().PostForm["csrf_token"]
	if want == "" || len(got) != 1 || !hmac.Equal([]byte(got[0]), []byte(want)) {
		return fmt.Errorf("invalid CSRF token")
	}
	return nil
}

// validateSkillFormOrigin is a defence in depth alongside the signed CSRF
// token. Referrer-Policy: no-referrer causes Chromium to serialize the Origin
// of a same-origin form POST as "null"; in that case Fetch Metadata is the
// browser-authenticated distinction between this page and a cross-site or
// sandboxed submitter. Missing metadata does not get the opaque-origin
// exemption (older/non-browser clients can omit Origin entirely and still must
// present the CSRF token).
func validateSkillFormOrigin(c echo.Context) error {
	origin := c.Request().Header.Get("Origin")
	if origin == "" {
		return nil
	}
	if origin == "null" {
		if c.Request().Header.Get("Sec-Fetch-Site") == "same-origin" {
			return nil
		}
		return fmt.Errorf("invalid origin")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host != c.Request().Host || u.Scheme != c.Scheme() || u.User != nil || u.Path != "" {
		return fmt.Errorf("invalid origin")
	}
	return nil
}

// Binding to the exact signed cookie invalidates tokens on session rotation.
func skillCSRFToken(c echo.Context, sm *SessionManager) string {
	if sm == nil {
		return ""
	}
	cookie, err := c.Cookie(pfSessionCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	mac := hmac.New(sha256.New, sm.secret)
	mac.Write([]byte("ui-skill-form-v1:"))
	mac.Write([]byte(cookie.Value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func parseSkillExpectedLatest(raw string) (int, bool) {
	var expected int
	if err := json.Unmarshal([]byte(raw), &expected); err != nil || expected < 0 {
		return 0, false
	}
	return expected, true
}

func handleUIPublishSkill(pool *pgxpool.Pool, sm *SessionManager) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		if err := parseSkillUIForm(c, sm); err != nil {
			return redirectSkillUI(c, c.Param("id"), "", err.Error())
		}
		expected, valid := parseSkillExpectedLatest(c.FormValue("expected_latest"))
		if !valid {
			return redirectSkillUI(c, c.Param("id"), "", "expected_latest must be a non-negative integer")
		}
		req := domain.PublishSkillVersionRequest{
			SkillID: c.Param("id"), ExpectedLatest: expected,
			Bundle: json.RawMessage(c.FormValue("bundle")), Contract: json.RawMessage(c.FormValue("contract")),
			ContentDigest: strings.TrimSpace(c.FormValue("content_digest")),
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		version, aerr := domain.PublishSkillVersion(ctx, pool, callerToUserRecord(u), req)
		if aerr != nil {
			return redirectSkillUI(c, req.SkillID, "", aerr.Message)
		}
		return redirectSkillUI(c, req.SkillID, fmt.Sprintf("Published version %d", version.Version), "")
	}
}

func handleUIShareSkill(pool *pgxpool.Pool, sm *SessionManager, revoke bool) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		if err := parseSkillUIForm(c, sm); err != nil {
			return redirectSkillUI(c, c.Param("id"), "", err.Error())
		}
		version, aerr := skillVersionParam(c)
		if aerr != nil {
			return redirectSkillUI(c, c.Param("id"), "", aerr.Message)
		}
		req := domain.SkillVersionShareRequest{
			SkillID: c.Param("id"), Version: version, Project: strings.TrimSpace(c.FormValue("project")),
		}
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		if revoke {
			aerr = domain.RevokeSkillVersionFromProject(ctx, pool, callerToUserRecord(u), req)
		} else {
			aerr = domain.ShareSkillVersionWithProject(ctx, pool, callerToUserRecord(u), req)
		}
		if aerr != nil {
			return redirectSkillUI(c, req.SkillID, "", aerr.Message)
		}
		action := "Shared"
		if revoke {
			action = "Revoked share for"
		}
		return redirectSkillUI(c, req.SkillID, fmt.Sprintf("%s version %d with project %s", action, version, req.Project), "")
	}
}

func handleUISkillVisibility(pool *pgxpool.Pool, sm *SessionManager) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := GetUser(c)
		if u == nil {
			return redirectToLogin(c)
		}
		if err := parseSkillUIForm(c, sm); err != nil {
			return redirectSkillUI(c, c.Param("id"), "", err.Error())
		}
		version, aerr := skillVersionParam(c)
		if aerr != nil {
			return redirectSkillUI(c, c.Param("id"), "", aerr.Message)
		}
		visibility := c.FormValue("visibility")
		ctx, cancel := contextWithTimeout(c)
		defer cancel()
		if _, aerr = domain.SetSkillVersionVisibility(ctx, pool, callerToUserRecord(u), c.Param("id"), version, visibility); aerr != nil {
			return redirectSkillUI(c, c.Param("id"), "", aerr.Message)
		}
		return redirectSkillUI(c, c.Param("id"), fmt.Sprintf("Version %d is now %s", version, visibility), "")
	}
}

func redirectSkillUI(c echo.Context, skillID, message, errorMessage string) error {
	values := url.Values{}
	if message != "" {
		values.Set("message", message)
	}
	if errorMessage != "" {
		values.Set("error", errorMessage)
	}
	location := "/ui/skills/" + url.PathEscape(skillID)
	if encoded := values.Encode(); encoded != "" {
		location += "?" + encoded
	}
	return c.Redirect(http.StatusSeeOther, location)
}
