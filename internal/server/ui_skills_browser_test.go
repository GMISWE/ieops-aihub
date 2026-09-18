package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// runUISkillsBrowser is opt-in through AIHUB_SKILLS_BROWSER_TEST and is called
// from the existing DB-gated UI route test. Keeping it under that top-level test
// avoids creating a second CI/DB-test inventory entry while still exercising a
// real Chromium renderer and browser-generated form requests.
func runUISkillsBrowser(t *testing.T, pool *pgxpool.Pool, ownerID, project string) {
	t.Helper()
	playwrightModule := os.Getenv("PLAYWRIGHT_NODE_MODULE")
	if playwrightModule == "" {
		t.Fatal("PLAYWRIGHT_NODE_MODULE must name the cached Playwright module directory")
	}
	if _, err := os.Stat(playwrightModule); err != nil {
		t.Fatalf("PLAYWRIGHT_NODE_MODULE: %v", err)
	}

	owner := &UserContext{UserID: ownerID, DisplayName: ownerID, Role: "writer"}
	created := skillRouteRequest(t, skillRouteDBServer(pool, owner), http.MethodPost, "/v1/skills", `{"name":"browser-ui-check"}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var skill struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &skill))

	const keyID = "k_ui_skills_browser"
	keys, err := json.Marshal([]map[string]any{{"id": keyID, "key_hash": "test-only-unused-hash"}})
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE users SET api_keys=$2 WHERE id=$1`, ownerID, keys)
	require.NoError(t, err)

	secret := []byte("test-only-ui-skills-browser-cookie-secret")
	sm := NewSessionManager(secret)
	e := echo.New()
	var requestHeadersMu sync.Mutex
	var requestHeaders [][2]string
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Method == http.MethodPost {
				requestHeadersMu.Lock()
				requestHeaders = append(requestHeaders, [2]string{
					c.Request().Header.Get("Origin"), c.Request().Header.Get("Sec-Fetch-Site"),
				})
				requestHeadersMu.Unlock()
			}
			return next(c)
		}
	})
	staticHandler := http.StripPrefix("/ui/static/", http.FileServer(staticFSRoot()))
	e.GET("/ui/static/*", echo.WrapHandler(cacheStatic(staticHandler)))
	ui := e.Group("/ui", uiSecurityHeaders(), RequireUISession(sm, pool))
	registerUISkillHandlers(ui, pool, parseTemplates(), sm)
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", uiSkillsPlaywrightScript)
	cmd.Env = append(os.Environ(),
		"PW_MODULE="+playwrightModule,
		"PW_BASE_URL="+ts.URL,
		"PW_COOKIE="+sm.Sign(ownerID, keyID, time.Hour),
		"PW_SKILL_ID="+skill.ID,
		"PW_PROJECT="+project,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Chromium acceptance timed out: %v\n%s", ctx.Err(), out)
	}
	require.NoError(t, err, "Chromium acceptance failed:\n%s", out)
	t.Logf("Chromium acceptance: %s", strings.TrimSpace(string(out)))
	requestHeadersMu.Lock()
	capturedHeaders := append([][2]string(nil), requestHeaders...)
	requestHeadersMu.Unlock()
	require.Len(t, capturedHeaders, 4)
	for _, headers := range capturedHeaders {
		require.Equal(t, [2]string{"null", "same-origin"}, headers,
			"Chromium's opaque Origin is safe here only with same-origin Fetch Metadata")
	}

	var versions, grants int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM skill_versions WHERE skill_id=$1`, skill.ID).Scan(&versions))
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM skill_version_grants WHERE skill_id=$1`, skill.ID).Scan(&grants))
	require.Equal(t, 1, versions, "the CSRF-rejected publish must not create a version")
	require.Zero(t, grants, "the browser revoke must remove the exact-version grant")
}

const uiSkillsPlaywrightScript = `
const { chromium } = require(process.env.PW_MODULE);
const assert = (ok, message) => { if (!ok) throw new Error(message); };
(async () => {
  const launch = { headless: true, args: ['--no-sandbox'] };
  if (process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE) launch.executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE;
  const browser = await chromium.launch(launch);
  try {
    const context = await browser.newContext();
    await context.addCookies([{ name: 'pf_session', value: process.env.PW_COOKIE, url: process.env.PW_BASE_URL, httpOnly: true, sameSite: 'Lax' }]);
    const page = await context.newPage();
    const posts = [];
    page.on('response', response => {
      if (response.request().method() === 'POST') posts.push({
        url: response.url(), status: response.status(), origin: response.request().headers()['origin']
      });
    });

    let response = await page.goto(process.env.PW_BASE_URL + '/ui/skills');
    assert(response.status() === 200, 'skill list status ' + response.status());
    assert((await page.locator('body').innerText()).includes('browser-ui-check'), 'skill missing from list DOM');
    await Promise.all([
      page.waitForNavigation(),
      page.locator('a[aria-label="Open skill browser-ui-check"]').click()
    ]);
    assert(page.url().endsWith('/ui/skills/' + process.env.PW_SKILL_ID), 'detail navigation failed: ' + page.url());

    const bundle = JSON.stringify({
      entry: 'SKILL.md',
      files: [{ path: 'SKILL.md', content: '</pre><script>window.__skillInjected=1</script>' }],
      license: { name: 'MIT' }
    });
    const contract = JSON.stringify({ capabilities: ['authoring'], runtime: { interactive: false } });
    const fillPublish = async () => {
      const form = page.locator('form[action$="/versions"]');
      await form.locator('input[name="expected_latest"]').fill('0');
      await form.locator('textarea[name="bundle"]').fill(bundle);
      await form.locator('textarea[name="contract"]').fill(contract);
      return form;
    };

    let form = await fillPublish();
    await form.locator('input[name="csrf_token"]').evaluate(node => node.remove());
    await Promise.all([page.waitForNavigation(), form.getByRole('button', { name: 'Publish private version' }).click()]);
    let text = await page.locator('body').innerText();
    assert(text.includes('invalid CSRF token'), 'missing-CSRF browser POST was not rejected; url=' + page.url() + '; body=' + text + '; posts=' + JSON.stringify(posts));
    assert(await page.locator('a[href$="/versions/1"]').count() === 0, 'CSRF-rejected publish created v1');

    form = await fillPublish();
    await Promise.all([page.waitForNavigation(), form.getByRole('button', { name: 'Publish private version' }).click()]);
    text = await page.locator('body').innerText();
    assert(text.includes('Published version 1'), 'successful publish message missing');
    await Promise.all([page.waitForNavigation(), page.locator('a[href$="/versions/1"]').click()]);
    const bundleText = await page.locator('pre').first().innerText();
    assert(bundleText.includes('</pre><script>window.__skillInjected=1</script>'), 'escaped bundle text missing from detail DOM');
    assert(await page.evaluate(() => window.__skillInjected) === undefined, 'escaped bundle executed script');
    assert(await page.locator('script').evaluateAll(nodes => nodes.every(n => !n.textContent.includes('__skillInjected'))) , 'bundle produced a script DOM node');

    let accessForm = page.locator('form[action$="/versions/1/shares"]');
    await accessForm.locator('input[name="project"]').fill(process.env.PW_PROJECT);
    await Promise.all([page.waitForNavigation(), accessForm.getByRole('button', { name: 'Share', exact: true }).click()]);
    text = await page.locator('body').innerText();
    assert(text.includes('Shared version 1 with project ' + process.env.PW_PROJECT), 'share result missing from DOM');

    accessForm = page.locator('form[action$="/versions/1/shares/revoke"]');
    await accessForm.locator('input[name="project"]').fill(process.env.PW_PROJECT);
    await Promise.all([page.waitForNavigation(), accessForm.getByRole('button', { name: 'Revoke', exact: true }).click()]);
    text = await page.locator('body').innerText();
    assert(text.includes('Revoked share for version 1 with project ' + process.env.PW_PROJECT), 'revoke result missing from DOM');

    assert(posts.length === 4 && posts.every(p => p.origin === 'null'),
      'unexpected browser Origin headers: ' + JSON.stringify(posts));
    const browserOrigin = posts[0].origin;
    const expected = [
      '/versions', '/versions', '/versions/1/shares', '/versions/1/shares/revoke'
    ];
    for (const suffix of expected) {
      const index = posts.findIndex(p => p.url.endsWith(suffix) && p.status === 303);
      assert(index >= 0, 'missing browser network POST 303 for ' + suffix + ': ' + JSON.stringify(posts));
      posts.splice(index, 1);
    }
    console.log(JSON.stringify({ browser: await browser.version(), list: 200, escaped: true, csrfRejected: true, published: true, shared: true, revoked: true, origin: browserOrigin }));
  } finally {
    await browser.close();
  }
})().catch(err => { console.error(err.stack || err); process.exit(1); });
`
