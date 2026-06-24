'use strict';

// Producer: a PostToolUse hook that maintains <workspace>/.polyforge/state/<wi_id>.chain.json
// from polyforge lifecycle MCP calls. The pure transition (applyEvent/mapStep) is unit-tested;
// the I/O wrapper (main) runs only when invoked directly by Claude Code.

const fs = require('fs');
const path = require('path');

function mapStep(stepId) {
  const s = String(stepId || '').toLowerCase();
  if (s.includes('spec')) return 'spec';
  if (s.includes('plan')) return 'plan';
  return 'execute';
}

function addUniq(arr, v) { return arr.includes(v) ? arr : arr.concat([v]); }

// Pure: given current chain state + an event, return the next state (null = delete the file).
function applyEvent(chain, toolName, toolInput, toolResponse) {
  const name = toolName.replace(/^mcp__plugin_polyforge_polyforge__/, '');
  switch (name) {
    case 'pf_update_step': {
      const station = mapStep(toolInput.step_id);
      // `execute` is a multi-step phase. Track per-step sub-progress in chain.exec and
      // keep the execute station ACTIVE until the attempt wraps — do NOT mark it
      // completed on the first execute step (that collapsed N steps into one green dot).
      if (station === 'execute') {
        const exec = chain.exec || { done: [], active: null };
        if (toolInput.status === 'in_progress') {
          return { ...chain, active: 'execute', exec: { done: exec.done, active: toolInput.step_id } };
        }
        if (toolInput.status === 'completed') {
          return { ...chain, active: 'execute', exec: { done: addUniq(exec.done, toolInput.step_id), active: null } };
        }
        return chain;
      }
      // spec / plan are single-step stations.
      if (toolInput.status === 'in_progress') return { ...chain, active: station };
      if (toolInput.status === 'completed') {
        return {
          ...chain,
          completed: addUniq(chain.completed, station),
          active: chain.active === station ? null : chain.active,
        };
      }
      return chain;
    }
    case 'pf_save_artifact': {
      if (toolInput.type === 'spec') return { ...chain, completed: addUniq(chain.completed, 'spec') };
      if (toolInput.type === 'methodology.plan') return { ...chain, completed: addUniq(chain.completed, 'plan') };
      return chain;
    }
    case 'pf_complete_attempt':
    case 'pf_wrap': {
      if (toolInput.status === 'paused') return { ...chain, status: 'paused' };
      return null; // wrapped / failed → delete
    }
    default:
      return chain;
  }
}

// ─── I/O wrapper ───
function readJSON(p) { try { return JSON.parse(fs.readFileSync(p, 'utf-8')); } catch { return null; } }

// Most recently claimed, currently-claimed wi.
function findActiveState(dir) {
  let files;
  try { files = fs.readdirSync(dir).filter((f) => f.endsWith('.json') && !f.endsWith('.chain.json')); }
  catch { return null; }
  const claimed = files.map((f) => readJSON(path.join(dir, f))).filter((s) => s && s.claimed);
  if (!claimed.length) return null;
  claimed.sort((a, b) => String(b.claimed_at || '').localeCompare(String(a.claimed_at || '')));
  return claimed[0];
}

// Resolve which wi's chain.json an event targets. Terminal events (complete_attempt/wrap)
// use toolInput.work_item_id because pf_complete_attempt deletes the state file BEFORE this
// hook runs — findActiveState would then miss it and the chain.json would leak (orphan).
function resolveWiId(shortName, toolInput, activeState) {
  const isTerminal = shortName === 'pf_complete_attempt' || shortName === 'pf_wrap';
  if (isTerminal && toolInput && toolInput.work_item_id) return toolInput.work_item_id;
  return (activeState && activeState.wi_id) || null;
}

function main() {
  let raw = '';
  try { raw = fs.readFileSync(0, 'utf-8'); } catch {}
  let evt = {};
  try { evt = JSON.parse(raw); } catch {}
  const toolName = evt.tool_name || evt.toolName || '';
  const toolInput = evt.tool_input || evt.toolInput || {};
  const toolResponse = evt.tool_response || evt.toolResponse || {};
  const cwd = evt.cwd || process.cwd();
  const dir = path.join(cwd, '.polyforge', 'state');

  const shortName = toolName.replace(/^mcp__plugin_polyforge_polyforge__/, '');
  const st = findActiveState(dir);
  const wiId = resolveWiId(shortName, toolInput, st);
  if (!wiId) return; // no target wi
  const chainPath = path.join(dir, wiId + '.chain.json');

  let chain = readJSON(chainPath);
  if (shortName === 'pf_claim_work_item') {
    if (!chain) {
      const worktree = st && st.worktrees ? Object.values(st.worktrees)[0] : '';
      chain = {
        wi: (st && st.slug) || wiId,
        worktree,
        completed: [],
        active: null,
        exec: { done: [], active: null },
        status: 'running',
        updated_at: new Date().toISOString(),
      };
      fs.writeFileSync(chainPath, JSON.stringify(chain));
    }
    return;
  }
  if (!chain) return;

  const next = applyEvent(chain, toolName, toolInput, toolResponse);
  if (next === null) {
    try { fs.unlinkSync(chainPath); } catch {}
  } else {
    next.updated_at = new Date().toISOString();
    fs.writeFileSync(chainPath, JSON.stringify(next));
  }
}

if (require.main === module) main();
module.exports = { applyEvent, mapStep, resolveWiId };
