'use strict';

// Pure renderer for the pf-work status chain (metro-line statusline segment).
// No I/O — takes chain data, returns a display string. Unit-tested in isolation.

const STATIONS = ['spec', 'plan', 'execute', 'wrap'];
const LABEL = { spec: 'spec', plan: 'plan', execute: 'exec', wrap: 'wrap' };
const DOT = { done: '🟢', active: '🟡', skipped: '⚪', pending: '⚪' };
const DIM = '\x1b[2m';
const RST = '\x1b[0m';

// Classify each station into done / active / skipped / pending.
// skipped = a not-completed station positioned BEFORE the active one (it was bypassed).
function classify(stations, completedSet, activeStation) {
  const activeIdx = activeStation ? stations.indexOf(activeStation) : -1;
  return stations.map((s, i) => {
    if (completedSet.has(s)) return 'done';
    if (s === activeStation) return 'active';
    if (activeIdx >= 0 && i < activeIdx) return 'skipped';
    return 'pending';
  });
}

function renderChain({ wi, stations = STATIONS, completed = [], active = null, subCounter = null }) {
  const completedSet = new Set(completed);
  const states = classify(stations, completedSet, active);
  const nodes = stations.map((s, i) => {
    const st = states[i];
    let label = LABEL[s] || s;
    if (st === 'skipped') label = DIM + '(' + label + ')' + RST;
    if (st === 'active' && s === 'execute' && subCounter) label = label + ' ' + subCounter;
    return DOT[st] + label;
  });
  return '▌pf ' + wi + '  ' + nodes.join('···');
}

module.exports = { classify, renderChain, STATIONS, LABEL };
