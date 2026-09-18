'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { applyEvent, mapStep, resolveWiId, successfulResponse } = require('../bin/pf-chain-hook.cjs');

const ok = { content: [{ type: 'text', text: '{"status":"ok"}' }] };
const base = () => ({ wi: 'aihub#71', completed: [], active: null, exec: { done: [], active: null }, status: 'running' });
const step = (chain, input, response = ok) => applyEvent(chain, 'polyforge_pf_update_step', input, response);

test('prefixes and legacy spec/plan artifact forms', () => {
  assert.equal(mapStep('write_spec'), 'spec');
  for (const prefix of ['', 'mcp__polyforge__', 'mcp__plugin_polyforge_polyforge__', 'polyforge-', 'polyforge_']) {
    assert.deepEqual(applyEvent(base(), prefix + 'pf_save_artifact', { type: 'methodology.spec' }, ok).completed, ['spec']);
    assert.deepEqual(applyEvent(base(), prefix + 'pf_save_artifact', { type: 'spec' }, ok).completed, ['spec']);
    assert.deepEqual(applyEvent(base(), prefix + 'pf_save_artifact', { type: 'methodology.plan' }, ok).completed, ['plan']);
  }
});

test('only confirmed successful responses advance or delete', () => {
  const input = { status: 'completed', step_id: 'write_spec' };
  // Refused: missing, empty, error-marked, or "success" with no actual result behind
  // it — a bridge that drops the real result must never advance the cache.
  for (const response of [null, {}, { isError: true, content: [] }, { error: 'bad' },
    { content: [{ type: 'text', text: '{"error":"denied"}' }] }, { success: false },
    { isError: false }, { content: [] }, { content: [], isError: false },
    { structuredContent: {} }, { result: null }, { result: '' }]) {
    assert.deepEqual(step(base(), input, response), base());
    assert.deepEqual(applyEvent(base(), 'pf_wrap', {}, response), base());
    assert.equal(successfulResponse(response), false);
  }
  // Accepted: explicit normalized status, or the actual non-empty error-free result.
  assert.deepEqual(step(base(), input).completed, ['spec']);
  assert.equal(successfulResponse({ status: 'ok' }), true);
  assert.equal(successfulResponse({ content: [{ type: 'text', text: 'plain prose is a result too' }] }), true);
  // The pi bridge's forward shape: the real content plus an explicit non-error status.
  assert.equal(successfulResponse({ content: [{ type: 'text', text: '{"status":"ok"}' }], isError: false }), true);
  // The opencode bridge's forward shape: the raw MCP CallToolResult it was handed.
  assert.equal(successfulResponse({ content: [{ type: 'text', text: '{"removed":["aihub"]}' }] }), true);
  assert.equal(successfulResponse({ structuredContent: { removed: ['aihub'] }, content: [] }), true);
  assert.equal(successfulResponse({ result: { removed: ['aihub'] } }), true);
  assert.equal(applyEvent(base(), 'pf_wrap', {}, ok), null);
});

test('fused next_step closes old step and opens successor atomically in cache', () => {
  let state = step(base(), { status: 'in_progress', step_id: 'write_spec' });
  state = step(state, { status: 'completed', step_id: 'write_spec', next_step: 'plan_steps' });
  assert.deepEqual(state.completed, ['spec']);
  assert.equal(state.active, 'plan');
  state = step(state, { status: 'completed', step_id: 'plan_steps', next_step: 'code_change' });
  assert.deepEqual(state.completed, ['spec', 'plan']);
  assert.equal(state.exec.active, 'code_change');
  state = step(state, { status: 'completed', step_id: 'code_change', next_step: 'code_review' });
  assert.deepEqual(state.exec.done, ['code_change']);
  assert.equal(state.exec.active, 'code_review');
  assert.equal(state.active, 'execute');
});

test('failure and heartbeat never mark a step done; pause clears transient activity', () => {
  const running = step(base(), { status: 'in_progress', step_id: 'code_change' });
  assert.deepEqual(step(running, { heartbeat: true, status: 'completed', step_id: 'code_change' }), running);
  const failed = step(running, { status: 'failed', step_id: 'code_change' });
  assert.deepEqual(failed.exec.done, []);
  assert.equal(failed.exec.active, null);
  const paused = applyEvent(running, 'pf_pause_attempt', {}, ok);
  assert.equal(paused.status, 'paused');
  assert.equal(paused.active, null);
  assert.equal(paused.exec.active, null);
  assert.equal(applyEvent(base(), 'pf_complete_attempt', { status: 'failed' }, ok), null);
});

test('exact WI routing never borrows a different recent claim', () => {
  const states = [{ wi_id: 'wi_A', slug: 'aihub#71' }, { wi_id: 'wi_B', slug: 'aihub#72' }];
  assert.equal(resolveWiId('pf_update_step', { work_item_id: 'aihub#71' }, states[1], states), 'wi_A');
  assert.equal(resolveWiId('pf_wrap', { work_item_id: 'wi_A' }, null), 'wi_A');
  assert.equal(resolveWiId('pf_update_step', {}, states[1], states), null);
  assert.equal(resolveWiId('pf_update_step', { work_item_id: 'aihub#73' }, states[1], states), null);
});

test('ambiguous credential or sidecar slugs never select an arbitrary WI', () => {
  const duplicateSlug = [{ wi_id: 'wi_A', slug: 'aihub#71' }, { wi_id: 'wi_B', slug: 'aihub#71' }];
  assert.equal(resolveWiId('pf_update_step', { work_item_id: 'aihub#71' }, null, duplicateSlug), null);
  const duplicateID = [{ wi_id: 'wi_A', slug: 'aihub#71' }, { wi_id: 'wi_A', slug: 'aihub#72' }];
  assert.equal(resolveWiId('pf_update_step', { work_item_id: 'wi_A' }, null, duplicateID), null);
});
