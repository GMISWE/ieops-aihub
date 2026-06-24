'use strict';

// Consumer: polyforge's standalone statusline. Composes any pre-existing statusline
// (e.g. ruflo) by running it with the same stdin, then appends the pf-work chain line.
// Renders nothing extra when there is no active running wi.

const fs = require('fs');
const path = require('path');
const cp = require('child_process');
const { renderChain } = require('./pf-chain-render.cjs');

function readJSON(p) { try { return JSON.parse(fs.readFileSync(p, 'utf-8')); } catch { return null; } }

function findActiveState(dir) {
  let files;
  try { files = fs.readdirSync(dir).filter((f) => f.endsWith('.json') && !f.endsWith('.chain.json')); }
  catch { return null; }
  const claimed = files.map((f) => readJSON(path.join(dir, f))).filter((s) => s && s.claimed);
  if (!claimed.length) return null;
  claimed.sort((a, b) => String(b.claimed_at || '').localeCompare(String(a.claimed_at || '')));
  return claimed[0];
}

// Run the pre-existing statusline (saved at install time) with the same stdin, return its output.
function runBase(stdinBuf, cwd) {
  const baseFile = path.join(cwd, '.polyforge', 'statusline-base');
  let cmd = '';
  try { cmd = fs.readFileSync(baseFile, 'utf-8').trim(); } catch { return ''; }
  if (!cmd) return '';
  try {
    const out = cp.execSync(cmd, {
      input: stdinBuf, cwd, stdio: ['pipe', 'pipe', 'ignore'], env: process.env, timeout: 5000,
    });
    return out.toString('utf-8').replace(/\n$/, '');
  } catch { return ''; }
}

// execute sub-progress comes from the pf_update_step event stream (chain.exec), maintained by
// pf-chain-hook.cjs — NOT from a file. pf-execute emits pf_update_step per step since aihub#101,
// so the chain hook receives every execute step; this also removes the old .pf_steps.json
// worktree-vs-parent location coupling.
function execSubCounter(chain) {
  const exec = chain && chain.exec;
  if (!exec || !Array.isArray(exec.done)) return null;
  const done = exec.done.length;
  return exec.total ? `${done}/${exec.total}` : `${done}`;
}

function chainLine(cwd) {
  const dir = path.join(cwd, '.polyforge', 'state');
  const st = findActiveState(dir);
  if (!st) return '';
  const chain = readJSON(path.join(dir, st.wi_id + '.chain.json'));
  if (!chain || chain.status === 'paused') return '';

  const active = chain.active;
  const subCounter = active === 'execute' ? execSubCounter(chain) : null;

  return renderChain({ wi: chain.wi, completed: chain.completed || [], active, subCounter });
}

function main() {
  let stdinBuf = Buffer.alloc(0);
  try { stdinBuf = fs.readFileSync(0); } catch {}
  const cwd = process.env.CLAUDE_PROJECT_DIR || process.cwd();

  const out = [];
  const base = runBase(stdinBuf, cwd);
  if (base) out.push(base);
  const line = chainLine(cwd);
  if (line) out.push(line);

  process.stdout.write(out.join('\n'));
}

main();
