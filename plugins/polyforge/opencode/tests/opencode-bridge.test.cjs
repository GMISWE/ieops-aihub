'use strict';

// Executable tests for the opencode hook bridge (aihub#653).
//
// WHY THESE EXIST. Mirrors tests/pi-bridge.test.cjs's own reasoning exactly: a matcher-table
// assertion (grep/JSON walk) can tell "a matcher exists for this tool name" but cannot tell
// "the guard's verdict is actually honoured" — a mutant that runs pf-commit-guard and then
// discards `reason` keeps a grep-based check green. The tests below drive the REAL
// createBridge() handlers against the REAL shared hook scripts (pf-commit-guard,
// pf-chain-hook.cjs), so that mutant fails here.
//
// WHERE THIS FILE LIVES, AND WHAT IT DOES NOT COVER (read before extending):
//   plugins/polyforge/tests/ (a sibling of opencode/) already holds one .test.cjs per bridge
//   (pi-bridge.test.cjs) plus the cross-harness "hop 2" matcher-routing contract inside
//   pf-commit-guard.test.sh (the `cases = {...}` table keyed by hooks.json / codex-hooks.json /
//   copilot-hooks.json / pi-hooks.json). aihub#653's file_scope lock covers EXACTLY
//   plugins/polyforge/opencode, internal/roles/render_opencode.go, internal/cli/roles_generate.go,
//   bin -- NOT plugins/polyforge/tests nor .github/workflows/ci.yml, so this wi could not add an
//   "opencode/opencode-hooks.json" row to that table, nor a CI step invoking this file (ci.yml
//   hardcodes one `node --test ... plugins/polyforge/tests/<file>` step per bridge, not a glob,
//   per .github/workflows/ci.yml:4886). Both are real, tracked follow-ups, filed (not just noted
//   in a comment) as aihub#659 (wi_9csQp7cM) from this wi's code_review step: whoever picks that
//   up should add the opencode row/step the same shape as pi's. Until it lands, this file is run
//   manually (`node --test plugins/polyforge/opencode/tests/opencode-bridge.test.cjs`) and by
//   this wi's own local gate, not by CI.
//
// SCOPE. This wi deliberately wires only tool.execute.before / tool.execute.after (see
// plugin/polyforge-hooks.js's own header comment for why session_start/input-router
// equivalents were deferred rather than approximated), so there is no session_start /
// before_agent_start / input suite here the way pi-bridge.test.cjs has one.

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '..', '..'); // plugins/polyforge (the TRUE plugin root)
const bridge = require(path.join(ROOT, 'opencode/plugin/polyforge-hooks.js'));
const HOOKS = JSON.parse(fs.readFileSync(path.join(ROOT, 'opencode/opencode-hooks.json'), 'utf-8')).hooks;

// pf-commit-guard is bash + an embedded python3 program; without python3 it is inert by
// design, which would turn every "must block" assertion below into a false failure.
let HAVE_PY = true;
try { execFileSync('python3', ['-c', 'pass'], { stdio: 'ignore' }); } catch { HAVE_PY = false; }

/** A tmpdir with its own .polyforge.yaml, so findWorkspaceRoot() succeeds hermetically —
 *  independent of where this checkout happens to sit relative to the real workspace root. */
function makeWorkspace() {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'oc-bridge-'));
  fs.writeFileSync(path.join(tmp, '.polyforge.yaml'), 'version: 1\n');
  return tmp;
}

/** Build the real Hooks object against the real opencode-hooks.json table. */
function mount(opts) {
  return bridge.createBridge(Object.assign({ pluginRoot: ROOT, hooks: HOOKS, directory: ROOT }, opts || {}));
}

const BANNED = 'fix cache bug\n\nCo-Authored-By: Claude <noreply@anthropic.com>';

const before = (toolName, args, opts) =>
  mount(opts)['tool.execute.before']({ tool: toolName, sessionID: 's', callID: 'c' }, { args: args || {} });

// ── the IR1 write gate actually decides ───────────────────────────────────────

test('tool.execute.before: a banned commit message is blocked, and the thrown message carries the guard\'s reason', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable, guard is inert by design');
  await assert.rejects(
    before('polyforge_pf_commit', { message: BANNED }),
    (err) => {
      assert.match(err.message, /polyforge IR1/);
      assert.match(err.message, /polyforge commit-guard/);
      assert.match(err.message, /AI-attribution/);
      return true;
    },
  );
});

// Negative control. Without it, "always throw" would pass the test above.
test('tool.execute.before: a clean commit message is NOT blocked', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  await assert.doesNotReject(before('polyforge_pf_commit', { message: 'fix race in reconnect logic' }));
});

test('tool.execute.before: pf_pr / pf_wrap / pf_ship are gated too', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  await assert.rejects(before('polyforge_pf_pr', { title: 'ok', body: 'AI-assisted fix' }));
  await assert.rejects(before('polyforge_pf_wrap', { pr_title: 'reviewed by sonnet', pr_body: 'ok' }));
  await assert.rejects(before('polyforge_pf_ship', { message: 'ok', pr_title: 'ok', pr_body: 'Generated with a bot' }));
});

// opencode's own MCP tool-id join (confirmed via opencode 1.18.30's compiled source, see
// opencode-hooks.json's _comment): sanitize(serverKey) + "_" + sanitize(toolName). Our
// shipped mcp.json.template names the server "polyforge", so this is the AS-SHIPPED,
// fully-gated-end-to-end spelling: pf-commit-guard's own strip recognizes literal
// "polyforge_" (shared with pi), so this must actually be blocked, not merely matched.
test('tool.execute.before: the as-shipped MCP tool id ("polyforge_pf_commit") is blocked end to end', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  for (const name of ['polyforge_pf_commit', 'pf_commit']) {
    await assert.rejects(before(name, { message: BANNED }), undefined, name + ' slipped past the gate');
  }
});

// KNOWN GAP, asserted honestly rather than papered over: this bridge's matcher is
// permissive on purpose (any *_pf_commit shape reaches pf-commit-guard even if a project
// renames the `mcp.polyforge` server entry), but pf-commit-guard's OWN prefix strip
// (hooks/pf-commit-guard, out of aihub#653's file_scope lock) does not recognize an
// arbitrary renamed key: it strips any `__`-terminated prefix, then checks literal
// `polyforge-` / `polyforge_` -- none of which matches `my_polyforge_server_`. So a renamed
// server entry reaches the guard but is NOT scanned, and the guard fails open (no fields ->
// exit 0, allow). This test pins that current, real behaviour rather than assuming the
// earlier (wrong) "downstream already strips any prefix" claim this file's comment used to
// make before this test caught it -- and rather than the second, subtler overclaim this
// comment itself made ("exactly three literal forms" for the `__`-strip case), caught by
// aihub#653's code_review.
test('tool.execute.before: a RENAMED server key currently fails OPEN (tracked gap, not this wi\'s fix)', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  await assert.doesNotReject(
    before('my_polyforge_server_pf_commit', { message: BANNED }),
    'if this now rejects, pf-commit-guard has learned to strip arbitrary server keys — ' +
      'promote this to a "must reject" case and delete this comment',
  );
});

test('tool.execute.before: the built-in bash tool is gated by its confirmed bare id "bash"', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  await assert.rejects(before('bash', { command: 'git commit -m "Co-Authored-By: Claude"' }));
  await assert.doesNotReject(before('bash', { command: 'ls -la' }));
});

test('tool.execute.before: the deny rule does not swallow ordinary tools', async () => {
  for (const name of ['read', 'grep', 'edit', 'polyforge_pf_get_work_item']) {
    await assert.doesNotReject(before(name, {}), name + ' was wrongly refused');
  }
});

// ── fail-open: a broken bridge must never wedge the session ───────────────────

test('tool.execute.before: a missing hook script fails OPEN, it does not throw', async () => {
  const h = mount({
    hooks: { 'tool.execute.before': [{ matcher: '^bash$', match: 'toolName', bash: '"/nonexistent/pf-guard"', inject: 'decision' }] },
  });
  await assert.doesNotReject(h['tool.execute.before']({ tool: 'bash', sessionID: 's', callID: 'c' }, { args: { command: 'x' } }));
});

test('tool.execute.before: a payload larger than the pipe buffer does not kill the process', async () => {
  // Reproduces the EPIPE path: a script that exits without reading stdin, plus >64 KiB.
  const h = mount({
    hooks: { 'tool.execute.before': [{ matcher: '^bash$', match: 'toolName', bash: 'exit 0', inject: 'decision' }] },
  });
  const huge = { command: 'x'.repeat(300 * 1024) };
  await assert.doesNotReject(h['tool.execute.before']({ tool: 'bash', sessionID: 's', callID: 'c' }, { args: huge }));
});

test('tool.execute.before: an unparseable matcher disables its own entry only', async () => {
  const h = mount({
    hooks: { 'tool.execute.before': [
      { matcher: '([unclosed', match: 'toolName', bash: 'echo \'{"permissionDecision":"deny","permissionDecisionReason":"never"}\'', inject: 'decision' },
      { matcher: '^bash$', match: 'toolName', bash: 'echo \'{"permissionDecision":"deny","permissionDecisionReason":"second rule still applies"}\'', inject: 'decision' },
    ] },
  });
  await assert.rejects(
    h['tool.execute.before']({ tool: 'bash', sessionID: 's', callID: 'c' }, { args: {} }),
    (err) => { assert.match(err.message, /second rule still applies/); return true; },
  );
});

test('an entry declaring the wrong match subject is skipped, not tested against the wrong string', async () => {
  // `match: "text"` under tool.execute.before is a config mistake — there is no "text"
  // subject for this event. Matching it against the tool name anyway would silently fire
  // a rule that was never meant to apply here.
  const h = mount({
    hooks: { 'tool.execute.before': [{ matcher: '^bash$', match: 'text', bash: 'echo \'{"permissionDecision":"deny"}\'' }] },
  });
  await assert.doesNotReject(h['tool.execute.before']({ tool: 'bash', sessionID: 's', callID: 'c' }, { args: {} }));
});

// ── payload shape: what the shared scripts actually read ──────────────────────

test('tool.execute.before payload uses the snake_case keys the guard reads, and carries cwd', async () => {
  const seen = [];
  const h = mount({
    hooks: { 'tool.execute.before': [{ matcher: '^bash$', match: 'toolName', bash: 'true', inject: 'decision' }] },
    exec: async (cmd, payload) => { seen.push(JSON.parse(payload)); return { stdout: '', stderr: '', timedOut: false, failed: false }; },
    directory: '/some/dir',
  });
  await h['tool.execute.before']({ tool: 'bash', sessionID: 's', callID: 'c' }, { args: { command: 'git commit' } });
  assert.deepStrictEqual(seen[0], { tool_name: 'bash', tool_input: { command: 'git commit' }, cwd: '/some/dir' });
});

test('tool.execute.after payload passes the WORKSPACE ROOT as cwd, not the worktree', async () => {
  // pf-chain-hook.cjs joins cwd with .polyforge/state directly — it does NOT walk up for
  // .polyforge.yaml itself. Handing it a worktree path makes it read a directory that does
  // not exist and return having written nothing.
  const tmp = makeWorkspace();
  const deep = path.join(tmp, 'pf.aihub-1', 'aihub');
  fs.mkdirSync(deep, { recursive: true });

  const seen = [];
  const h = mount({
    hooks: { 'tool.execute.after': [{ matcher: '^polyforge_pf_update_step$', match: 'toolName', bash: 'true' }] },
    exec: async (cmd, payload) => { seen.push(JSON.parse(payload)); return { stdout: '', stderr: '', timedOut: false, failed: false }; },
    directory: deep,
  });
  await h['tool.execute.after']({ tool: 'polyforge_pf_update_step', sessionID: 's', callID: 'c', args: { step_id: 's' } }, {});
  assert.strictEqual(seen[0].cwd, fs.realpathSync(tmp), 'cwd must be the workspace root');
  fs.rmSync(tmp, { recursive: true, force: true });
});

test('tool.execute.after: no workspace root found -> silently does nothing (no throw, no exec)', async () => {
  let ran = false;
  const h = mount({
    hooks: { 'tool.execute.after': [{ matcher: '.*', match: 'toolName', bash: 'true' }] },
    exec: async () => { ran = true; return { stdout: '', stderr: '', timedOut: false, failed: false }; },
    directory: '/', // never has a .polyforge.yaml ancestor
  });
  await assert.doesNotReject(h['tool.execute.after']({ tool: 'polyforge_pf_wrap', sessionID: 's', callID: 'c', args: {} }, {}));
  assert.strictEqual(ran, false);
});

test('the hook environment does not announce opencode as Codex or Cursor, and marks POLYFORGE_RUNTIME', async () => {
  const prev = { PLUGIN_ROOT: process.env.PLUGIN_ROOT, CURSOR_PLUGIN_ROOT: process.env.CURSOR_PLUGIN_ROOT };
  process.env.PLUGIN_ROOT = '/codex/announces/itself/this/way';
  process.env.CURSOR_PLUGIN_ROOT = '/cursor/does/this';
  try {
    let env = null;
    const h = mount({
      hooks: { 'tool.execute.before': [{ matcher: '^bash$', match: 'toolName', bash: 'true', inject: 'decision' }] },
      exec: async (cmd, payload, e) => { env = e; return { stdout: '', stderr: '', timedOut: false, failed: false }; },
    });
    await h['tool.execute.before']({ tool: 'bash', sessionID: 's', callID: 'c' }, { args: {} });
    assert.ok(!('PLUGIN_ROOT' in env), 'PLUGIN_ROOT leaked: opencode would be read as Codex');
    assert.ok(!('CURSOR_PLUGIN_ROOT' in env), 'CURSOR_PLUGIN_ROOT leaked: opencode would be read as Cursor');
    assert.strictEqual(env.CLAUDE_PLUGIN_ROOT, ROOT);
    assert.strictEqual(env.POLYFORGE_RUNTIME, 'opencode');
  } finally {
    for (const [k, v] of Object.entries(prev)) {
      if (v === undefined) delete process.env[k]; else process.env[k] = v;
    }
  }
});

// ── discovery: the nested opencode-hooks.json path (the bug caught in review) ─

test('hooksJsonPath points one level under pluginRoot, not at pluginRoot itself', () => {
  assert.strictEqual(bridge.hooksJsonPath('/x'), path.join('/x', 'opencode', 'opencode-hooks.json'));
});

test('resolvePluginRoot finds the real checkout root (a sibling of opencode/, holding plugin.json)', () => {
  const found = bridge.resolvePluginRoot();
  assert.strictEqual(found, ROOT);
});

test('loadHookConfig reads through the nested path and returns {} on any failure', () => {
  const hooks = bridge.loadHookConfig(ROOT);
  assert.ok(hooks['tool.execute.before'] && hooks['tool.execute.before'].length > 0);
  assert.deepStrictEqual(bridge.loadHookConfig('/nonexistent'), {});
});

// ── pure helpers ──────────────────────────────────────────────────────────────

test('extractDeny reads both shapes the guard emits, and nothing else', () => {
  assert.strictEqual(bridge.extractDeny({ permissionDecision: 'deny', permissionDecisionReason: 'flat' }), 'flat');
  assert.strictEqual(
    bridge.extractDeny({ hookSpecificOutput: { permissionDecision: 'deny', permissionDecisionReason: 'nested' } }),
    'nested',
  );
  assert.strictEqual(bridge.extractDeny({ permissionDecision: 'allow', permissionDecisionReason: 'x' }), null);
  for (const v of [null, undefined, '', 42, {}]) assert.strictEqual(bridge.extractDeny(v), null);
});

test('parseJSON never throws', () => {
  assert.strictEqual(bridge.parseJSON('not json'), null);
  assert.strictEqual(bridge.parseJSON(''), null);
  assert.strictEqual(bridge.parseJSON(undefined), null);
  assert.deepStrictEqual(bridge.parseJSON('{"a":1}'), { a: 1 });
});

test('findWorkspaceRoot walks up and gives up at the filesystem root', () => {
  assert.strictEqual(bridge.findWorkspaceRoot('/'), null);
  assert.strictEqual(bridge.findWorkspaceRoot('/nonexistent/deep/path'), null);
});
