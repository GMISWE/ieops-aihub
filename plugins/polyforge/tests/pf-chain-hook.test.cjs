'use strict';
const { test } = require('node:test');
const assert = require('node:assert');
const { applyEvent, mapStep, resolveWiId } = require('../bin/pf-chain-hook.cjs');

const base = () => ({ wi: 'aihub#71', worktree: '/w', completed: [], active: null, status: 'running' });

test('mapStep maps step ids to macro stations', () => {
  assert.strictEqual(mapStep('write_spec'), 'spec');
  assert.strictEqual(mapStep('plan_steps'), 'plan');
  assert.strictEqual(mapStep('code_change'), 'execute');
});

test('pf_update_step in_progress sets active', () => {
  const s = applyEvent(base(), 'pf_update_step', { status: 'in_progress', step_id: 'write_spec' }, {});
  assert.strictEqual(s.active, 'spec');
});

test('pf_update_step completed adds to completed and clears active', () => {
  const start = { ...base(), active: 'spec' };
  const s = applyEvent(start, 'pf_update_step', { status: 'completed', step_id: 'write_spec' }, {});
  assert.ok(s.completed.includes('spec'));
  assert.strictEqual(s.active, null);
});

test('pf_save_artifact type=spec marks spec done', () => {
  const s = applyEvent(base(), 'pf_save_artifact', { type: 'spec' }, {});
  assert.ok(s.completed.includes('spec'));
});

test('pf_save_artifact type=methodology.plan marks plan done', () => {
  const s = applyEvent(base(), 'pf_save_artifact', { type: 'methodology.plan' }, {});
  assert.ok(s.completed.includes('plan'));
});

test('pause sets status=paused (does not delete)', () => {
  const s = applyEvent(base(), 'pf_complete_attempt', { status: 'paused' }, {});
  assert.strictEqual(s.status, 'paused');
});

test('wrap returns null (delete chain.json)', () => {
  const s = applyEvent(base(), 'pf_wrap', { status: 'wrapped' }, {});
  assert.strictEqual(s, null);
});

test('completed is deduped', () => {
  let s = applyEvent(base(), 'pf_save_artifact', { type: 'spec' }, {});
  s = applyEvent(s, 'pf_save_artifact', { type: 'spec' }, {});
  assert.deepStrictEqual(s.completed, ['spec']);
});

test('full prefix tool name is handled', () => {
  const s = applyEvent(base(), 'mcp__plugin_polyforge_polyforge__pf_save_artifact', { type: 'spec' }, {});
  assert.ok(s.completed.includes('spec'));
});

test('Codex prefix tool name (mcp__polyforge__) is handled', () => {
  const s = applyEvent(base(), 'mcp__polyforge__pf_save_artifact', { type: 'spec' }, {});
  assert.ok(s.completed.includes('spec'));
});

test('Copilot prefix tool name (polyforge-) is handled', () => {
  const s = applyEvent(base(), 'polyforge-pf_save_artifact', { type: 'spec' }, {});
  assert.ok(s.completed.includes('spec'));
});

// pi names MCP tools `<server>_<tool>` with an UNDERSCORE and no "__" separator, so it
// matches neither the two mcp__ forms nor Copilot's `polyforge-`. Left unstripped, every
// case below falls to `default: return chain` and the chain file is never advanced —
// silently, with exit 0. Same shape as the aihub#503 finding in pf-commit-guard.
test('pi prefix tool name (polyforge_) is handled', () => {
  const s = applyEvent(base(), 'polyforge_pf_save_artifact', { type: 'spec' }, {});
  assert.ok(s.completed.includes('spec'));
});

test('pi prefix: update_step and wrap fire identically', () => {
  const s = applyEvent(base(), 'polyforge_pf_update_step', { status: 'in_progress', step_id: 'write_spec' }, {});
  assert.strictEqual(s.active, 'spec');
  assert.strictEqual(applyEvent(base(), 'polyforge_pf_wrap', { status: 'wrapped' }, {}), null);
});

// Negative control: stripping must not be so greedy that an unrelated tool whose name
// merely starts with the server prefix turns into a lifecycle transition.
test('pi prefix: unrelated polyforge_ tool is inert', () => {
  const start = base();
  assert.deepStrictEqual(applyEvent(start, 'polyforge_pf_get_work_item', { type: 'spec' }, {}), start);
});

// ─── execute is a multi-step phase (event-driven sub-progress, no premature green) ───

test('execute in_progress keeps execute active + records exec.active (not in completed)', () => {
  const s = applyEvent(base(), 'pf_update_step', { status: 'in_progress', step_id: 'code_change' }, {});
  assert.strictEqual(s.active, 'execute');
  assert.strictEqual(s.exec.active, 'code_change');
  assert.deepStrictEqual(s.exec.done, []);
  assert.ok(!s.completed.includes('execute'));
});

test('execute completed accumulates in exec.done, execute STAYS active, never in completed', () => {
  let s = applyEvent(base(), 'pf_update_step', { status: 'in_progress', step_id: 'code_change' }, {});
  s = applyEvent(s, 'pf_update_step', { status: 'completed', step_id: 'code_change' }, {});
  assert.strictEqual(s.active, 'execute');            // not cleared after first step
  assert.deepStrictEqual(s.exec.done, ['code_change']);
  assert.strictEqual(s.exec.active, null);
  assert.ok(!s.completed.includes('execute'));         // Gap B: no premature green
  s = applyEvent(s, 'pf_update_step', { status: 'in_progress', step_id: 'code_review' }, {});
  s = applyEvent(s, 'pf_update_step', { status: 'completed', step_id: 'code_review' }, {});
  assert.deepStrictEqual(s.exec.done, ['code_change', 'code_review']);
  assert.strictEqual(s.active, 'execute');
});

test('exec.done is deduped on repeat completion', () => {
  let s = applyEvent(base(), 'pf_update_step', { status: 'completed', step_id: 'code_change' }, {});
  s = applyEvent(s, 'pf_update_step', { status: 'completed', step_id: 'code_change' }, {});
  assert.deepStrictEqual(s.exec.done, ['code_change']);
});

// ─── resolveWiId: terminal events must not rely on findActiveState (Gap C) ───

test('resolveWiId: terminal event uses toolInput.work_item_id even when state file gone', () => {
  assert.strictEqual(resolveWiId('pf_complete_attempt', { work_item_id: 'wi_X' }, null), 'wi_X');
  assert.strictEqual(resolveWiId('pf_wrap', { work_item_id: 'wi_X' }, null), 'wi_X');
});

test('resolveWiId: non-terminal event uses the active state wi_id', () => {
  assert.strictEqual(resolveWiId('pf_update_step', {}, { wi_id: 'wi_Y' }), 'wi_Y');
  assert.strictEqual(resolveWiId('pf_update_step', { work_item_id: 'wi_X' }, { wi_id: 'wi_Y' }), 'wi_Y');
});

test('resolveWiId: terminal falls back to active state when no toolInput wi; null when neither', () => {
  assert.strictEqual(resolveWiId('pf_complete_attempt', {}, { wi_id: 'wi_Z' }), 'wi_Z');
  assert.strictEqual(resolveWiId('pf_update_step', {}, null), null);
});
