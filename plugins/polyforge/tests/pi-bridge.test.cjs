'use strict';

// Executable tests for the pi hook bridge (aihub#503).
//
// WHY THESE EXIST. tests/pi-runtime.test.sh asserts over the bridge with `grep`, and grep
// cannot tell "the gate is wired" from "the gate is wired and its verdict is thrown away".
// Concrete mutant that keeps every grep-based check green: delete
//     if (reason) return { block: true, reason };
// from the tool_call handler — i.e. run pf-commit-guard, then ignore what it said. The
// `grep -q 'block: true'` check still passes, because the mcp/mcpScript deny line matches.
// IR1 would be off under pi with a fully green CI. The tests below drive the REAL handlers
// against the REAL hook scripts, so that mutant fails here.
//
// This is also why the bridge is CommonJS rather than the .ts pi's examples use: pi loads
// index.ts and index.js identically (both via jiti), and CJS additionally means `node --test`
// can require it with no transpiler and no version-gated flag.

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '..');            // plugins/polyforge
const bridge = require(path.join(ROOT, 'pi/extensions/polyforge/index.js'));
const HOOKS = JSON.parse(fs.readFileSync(path.join(ROOT, 'pi-hooks.json'), 'utf-8')).hooks;

// The guard is bash + an embedded python3 program; without python3 it is inert by design,
// which would turn every "must block" assertion below into a false failure.
let HAVE_PY = true;
try { execFileSync('python3', ['-c', 'pass'], { stdio: 'ignore' }); } catch { HAVE_PY = false; }

/** Install the real handlers against a fake pi, returning them by event name. */
function mount(opts) {
  const handlers = {};
  const pi = { on: (name, fn) => { handlers[name] = fn; } };
  bridge.createBridge(pi, Object.assign({ pluginRoot: ROOT, hooks: HOOKS }, opts || {}));
  return handlers;
}

const call = (toolName, input, cwd) =>
  mount().tool_call({ toolName, input: input || {} }, { cwd: cwd || ROOT });

const BANNED = 'fix cache bug\n\nCo-Authored-By: Claude <noreply@anthropic.com>';

// ── the IR1 write gate actually decides ───────────────────────────────────────

test('tool_call: a banned commit message is blocked with the guard\'s own reason', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable, guard is inert by design');
  const r = await call('polyforge_pf_commit', { message: BANNED });
  assert.ok(r && r.block === true, 'expected a block, got ' + JSON.stringify(r));
  assert.match(r.reason, /polyforge commit-guard/);
  assert.match(r.reason, /AI-attribution/);
});

// Negative control. Without it, "always return {block:true}" would pass the test above.
test('tool_call: a clean commit message is NOT blocked', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  assert.strictEqual(await call('polyforge_pf_commit', { message: 'fix race in reconnect logic' }), undefined);
});

test('tool_call: pf_pr / pf_wrap / pf_ship are gated too', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  const pr = await call('polyforge_pf_pr', { title: 'ok', body: 'AI-assisted fix' });
  const wrap = await call('polyforge_pf_wrap', { pr_title: 'reviewed by sonnet', pr_body: 'ok' });
  const ship = await call('polyforge_pf_ship', { message: 'ok', pr_title: 'ok', pr_body: 'Generated with a bot' });
  for (const [n, r] of [['pf_pr', pr], ['pf_wrap', wrap], ['pf_ship', ship]]) {
    assert.ok(r && r.block === true, n + ' was not blocked: ' + JSON.stringify(r));
  }
});

// pi-mcp-adapter's toolPrefix setting (global OR per-server) changes the emitted spelling.
// The matcher accepts all three; the guard normalises all three to pf_commit.
test('tool_call: every toolPrefix spelling reaches the gate', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  for (const name of ['polyforge_pf_commit', 'pf_commit', 'mcp__polyforge_pf_commit']) {
    const r = await call(name, { message: BANNED });
    assert.ok(r && r.block === true, name + ' slipped past the gate');
  }
});

test('tool_call: shell tools are gated, and pi has TWO of them', async (t) => {
  if (!HAVE_PY) return t.skip('python3 unavailable');
  const cmd = { command: 'git commit -m "Co-Authored-By: Claude"' };
  for (const shell of ['bash', 'powershell']) {
    const r = await call(shell, cmd);
    assert.ok(r && r.block === true, shell + ' is not gated: ' + JSON.stringify(r));
  }
  // ...and an ordinary command through either is untouched.
  assert.strictEqual(await call('bash', { command: 'ls -la' }), undefined);
});

// ── the bypass doors ──────────────────────────────────────────────────────────

test('tool_call: mcp and mcpScript are refused outright, without running anything', async () => {
  for (const name of ['mcp', 'mcpScript']) {
    const r = await call(name, { tool: 'pf_commit', args: {} });
    assert.ok(r && r.block === true, name + ' was not refused');
    assert.match(r.reason, /polyforge IR1/);
  }
});

test('tool_call: the deny rule does not swallow ordinary tools', async () => {
  for (const name of ['read', 'grep', 'mcp_something', 'polyforge_pf_get_work_item']) {
    assert.strictEqual(await call(name, {}), undefined, name + ' was wrongly refused');
  }
});

// ── fail-open: a broken bridge must never wedge the session ───────────────────

test('tool_call: a missing hook script fails OPEN, it does not throw or block', async () => {
  const handlers = mount({
    hooks: { tool_call: [{ matcher: '^bash$', match: 'toolName', bash: '"/nonexistent/pf-guard"', inject: 'decision' }] },
  });
  assert.strictEqual(await handlers.tool_call({ toolName: 'bash', input: { command: 'x' } }, { cwd: ROOT }), undefined);
});

test('tool_call: a payload larger than the pipe buffer does not kill the process', async () => {
  // Reproduces the EPIPE path: a script that exits without reading stdin, plus >64 KiB.
  // Unhandled, node raises an uncaught 'error' event and pi (which installs no
  // uncaughtException handler) dies.
  const handlers = mount({
    hooks: { tool_call: [{ matcher: '^bash$', match: 'toolName', bash: 'exit 0', inject: 'decision' }] },
  });
  const huge = { command: 'x'.repeat(300 * 1024) };
  assert.strictEqual(await handlers.tool_call({ toolName: 'bash', input: huge }, { cwd: ROOT }), undefined);
});

test('tool_call: an unparseable matcher disables its own entry only', async () => {
  const handlers = mount({
    hooks: { tool_call: [
      { matcher: '([unclosed', match: 'toolName', deny: 'never' },
      { matcher: '^bash$', match: 'toolName', deny: 'second rule still applies' },
    ] },
  });
  const r = await handlers.tool_call({ toolName: 'bash', input: {} }, { cwd: ROOT });
  assert.ok(r && r.block === true);
  assert.strictEqual(r.reason, 'second rule still applies');
});

// ── payload shape: what the scripts actually read ─────────────────────────────

test('tool_call payload uses the snake_case keys the guard reads, and carries cwd', async () => {
  const seen = [];
  const handlers = mount({
    hooks: { tool_call: [{ matcher: '^bash$', match: 'toolName', bash: 'true', inject: 'decision' }] },
    exec: async (cmd, payload) => { seen.push(JSON.parse(payload)); return { stdout: '', stderr: '', failed: false }; },
  });
  await handlers.tool_call({ toolName: 'bash', input: { command: 'git commit' } }, { cwd: '/some/dir' });
  assert.deepStrictEqual(seen[0], { tool_name: 'bash', tool_input: { command: 'git commit' }, cwd: '/some/dir' });
});

test('tool_result payload passes the WORKSPACE ROOT as cwd, not the worktree', async () => {
  // pf-chain-hook.cjs joins cwd with .polyforge/state directly — it does NOT walk up for
  // .polyforge.yaml the way the other two scripts do. Handing it a worktree path makes it
  // read a directory that does not exist and return having written nothing.
  const tmp = fs.mkdtempSync(path.join(require('node:os').tmpdir(), 'pf-bridge-'));
  fs.writeFileSync(path.join(tmp, '.polyforge.yaml'), 'version: 1\n');
  const deep = path.join(tmp, 'pf.aihub-1', 'aihub');
  fs.mkdirSync(deep, { recursive: true });

  const seen = [];
  const handlers = mount({
    hooks: { tool_result: [{ matcher: '^polyforge_pf_update_step$', match: 'toolName', bash: 'true' }] },
    exec: async (cmd, payload) => { seen.push(JSON.parse(payload)); return { stdout: '', stderr: '', failed: false }; },
  });
  await handlers.tool_result({ toolName: 'polyforge_pf_update_step', input: { step_id: 's' } }, { cwd: deep });
  assert.strictEqual(seen[0].cwd, fs.realpathSync(tmp), 'cwd must be the workspace root');
  fs.rmSync(tmp, { recursive: true, force: true });
});

test('the hook environment does not announce pi as Codex or Cursor', async () => {
  // These must be SET in the ambient environment first, or the assertions below pass
  // whether or not the bridge unsets them — the runner simply does not define them, and
  // the check quietly measures nothing. (Caught by mutation testing: deleting the
  // `delete env.PLUGIN_ROOT` line left this test green.)
  const prev = { PLUGIN_ROOT: process.env.PLUGIN_ROOT, CURSOR_PLUGIN_ROOT: process.env.CURSOR_PLUGIN_ROOT };
  process.env.PLUGIN_ROOT = '/codex/announces/itself/this/way';
  process.env.CURSOR_PLUGIN_ROOT = '/cursor/does/this';
  try {
    let env = null;
    const handlers = mount({
      hooks: { session_start: [{ matcher: '.*', match: 'always', bash: 'true', inject: 'context' }] },
      exec: async (cmd, payload, e) => { env = e; return { stdout: '', stderr: '', failed: false }; },
    });
    await handlers.session_start({}, { cwd: ROOT });
    // pf-session-start selects its OUTPUT SHAPE from these two variables:
    //   if CURSOR_PLUGIN_ROOT -> Cursor shape; elif PLUGIN_ROOT -> Codex shape; else both keys.
    // Leaking either one makes it emit a shape whose top-level additionalContext the bridge
    // then fails to find — the injected payload silently disappears.
    assert.ok(!('PLUGIN_ROOT' in env), 'PLUGIN_ROOT leaked: pi would be read as Codex');
    assert.ok(!('CURSOR_PLUGIN_ROOT' in env), 'CURSOR_PLUGIN_ROOT leaked: pi would be read as Cursor');
    assert.strictEqual(env.CLAUDE_PLUGIN_ROOT, ROOT);
    assert.strictEqual(env.POLYFORGE_RUNTIME, 'pi');
  } finally {
    for (const [k, v] of Object.entries(prev)) {
      if (v === undefined) delete process.env[k]; else process.env[k] = v;
    }
  }
});

// ── input -> skill routing ────────────────────────────────────────────────────

test('input: a /pf-* line routes the bare skill name, which is what the router matches', async () => {
  const seen = [];
  const handlers = mount({
    exec: async (cmd, payload) => { seen.push({ cmd, p: JSON.parse(payload) }); return { stdout: '', stderr: '', failed: false }; },
  });
  await handlers.input({ text: '/pf-execute go' }, { cwd: ROOT });
  assert.strictEqual(seen.length, 1);
  assert.match(seen[0].cmd, /pf-skill-router/);
  // pf-skill-router reads exactly tool_input.skill and splits on ":", so a bare name hits.
  assert.deepStrictEqual(seen[0].p.tool_input, { skill: 'pf-execute' });
});

test('input: the polyforge: prefix form routes identically; ordinary prose does not route', async () => {
  const seen = [];
  const handlers = mount({
    exec: async (cmd, payload) => { seen.push(JSON.parse(payload).tool_input.skill); return { stdout: '', stderr: '', failed: false }; },
  });
  await handlers.input({ text: '/polyforge:pf-spec scope it' }, { cwd: ROOT });
  await handlers.input({ text: 'please run /pf-execute later' }, { cwd: ROOT }); // not at line start
  await handlers.input({ text: 'hello there' }, { cwd: ROOT });
  assert.deepStrictEqual(seen, ['pf-spec']);
});

test('input never transforms or swallows what the user typed', async () => {
  const handlers = mount({ exec: async () => ({ stdout: '', stderr: '', failed: false }) });
  assert.strictEqual(await handlers.input({ text: '/pf-execute go' }, { cwd: ROOT }), undefined);
});

// ── match/kind agreement ──────────────────────────────────────────────────────

test('an entry declaring the wrong match subject is skipped, not tested against the wrong string', async () => {
  // `match: "text"` under tool_call is a config mistake. Matching it against the tool name
  // anyway would produce a rule that silently never fires.
  const handlers = mount({
    hooks: { tool_call: [{ matcher: '^bash$', match: 'text', deny: 'must not fire' }] },
  });
  assert.strictEqual(await handlers.tool_call({ toolName: 'bash', input: {} }, { cwd: ROOT }), undefined);
});

// ── context injection reaches the turn ────────────────────────────────────────

test('session_start context is drained into the before_agent_start message', async () => {
  const payload = 'IR1 — work-item-gated writes';
  const handlers = mount({
    hooks: { session_start: [{ matcher: '.*', match: 'always', bash: 'true', inject: 'context' }] },
    exec: async () => ({ stdout: JSON.stringify({ additionalContext: payload }), stderr: '', failed: false }),
  });
  await handlers.session_start({}, { cwd: ROOT });
  const msg = await handlers.before_agent_start({}, { cwd: ROOT });
  assert.strictEqual(msg.message.content, payload);
  // display is a boolean in pi's own types; a string would be truthy and print the payload.
  assert.strictEqual(msg.message.display, false);
  // Drained: a second turn must not repeat it.
  assert.strictEqual(await handlers.before_agent_start({}, { cwd: ROOT }), undefined);
});

test('the nested hookSpecificOutput shape is accepted as well as the flat one', async () => {
  const handlers = mount({
    hooks: { session_start: [{ matcher: '.*', match: 'always', bash: 'true', inject: 'context' }] },
    exec: async () => ({
      stdout: JSON.stringify({ hookSpecificOutput: { hookEventName: 'SessionStart', additionalContext: 'nested-only' } }),
      stderr: '', failed: false,
    }),
  });
  await handlers.session_start({}, { cwd: ROOT });
  assert.strictEqual((await handlers.before_agent_start({}, { cwd: ROOT })).message.content, 'nested-only');
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

test('extractContext ignores empty and non-string context', () => {
  assert.strictEqual(bridge.extractContext({ additionalContext: '   ' }), null);
  assert.strictEqual(bridge.extractContext({ additionalContext: 42 }), null);
  assert.strictEqual(bridge.extractContext(null), null);
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
