'use strict';
const { test } = require('node:test');
const assert = require('node:assert');
const { classify, renderChain, STATIONS } = require('../bin/pf-chain-render.cjs');

// strip ANSI for assertions on visible text
const strip = (s) => s.replace(/\x1b\[[0-9;]*m/g, '');

test('classify: rhs=true reached execute, spec/plan done', () => {
  const states = classify(STATIONS, new Set(['spec', 'plan']), 'execute');
  assert.deepStrictEqual(states, ['done', 'done', 'active', 'pending']);
});

test('classify: rhs=false skipped spec/plan (pending-before-active = skipped)', () => {
  const states = classify(STATIONS, new Set([]), 'execute');
  assert.deepStrictEqual(states, ['skipped', 'skipped', 'active', 'pending']);
});

test('classify: just claimed, no active, all pending', () => {
  const states = classify(STATIONS, new Set([]), null);
  assert.deepStrictEqual(states, ['pending', 'pending', 'pending', 'pending']);
});

test('renderChain: done stations use 🟢 + normal label, slug first', () => {
  const v = strip(renderChain({ wi: 'aihub#71', completed: ['spec', 'plan'], active: 'execute' }));
  assert.ok(v.startsWith('▌pf aihub#71'), v);
  assert.ok(v.includes('🟢spec'), v);
  assert.ok(v.includes('🟢plan'), v);
  assert.ok(v.includes('🟡exec'), v);
  assert.ok(v.includes('⚪wrap'), v);
  assert.ok(v.includes('···'), v);
});

test('renderChain: skipped stations get parenthesized label', () => {
  const v = strip(renderChain({ wi: 'aihub#71', completed: [], active: 'execute' }));
  assert.ok(v.includes('⚪(spec)'), v);
  assert.ok(v.includes('⚪(plan)'), v);
  assert.ok(v.includes('🟡exec'), v);
});

test('renderChain: execute sub-counter', () => {
  const v = strip(renderChain({ wi: 'aihub#71', completed: [], active: 'execute', subCounter: '4/6' }));
  assert.ok(v.includes('🟡exec 4/6'), v);
});
