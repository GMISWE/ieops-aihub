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

// Every way a runtime can spell the same polyforge MCP tool. Claude Code uses
// mcp__plugin_polyforge_polyforge__, Codex mcp__polyforge__, Copilot CLI `polyforge-`,
// and pi `polyforge_` (aihub#503) — server name, separator, no "__" at all.
//
// ONE table, used by both call sites below. It used to be an inline literal written out
// twice, which is how pi's spelling could be added to a matcher elsewhere while this file
// kept silently falling through to `default: return chain` — no error, exit 0, and a
// chain.json that simply never advances.
const SERVER_PREFIX_RE = /^(?:mcp__(?:plugin_polyforge_polyforge|polyforge)__|polyforge[-_])/;
function stripServerPrefix(toolName) {
  return String(toolName || '').replace(SERVER_PREFIX_RE, '');
}

// A tool response counts as success ONLY when success is explicit: an
// explicit normalized status ('ok'/'success'), or the ACTUAL result the tool
// produced — a NON-EMPTY content array (or structured/result body) that carries
// no error marker in any spelling. An empty object, an empty content array or
// a bare `{isError:false}` from a bridge that dropped the real result is NOT
// success: a display cache advanced by an unconfirmed tool call lies about the
// attempt, so unconfirmed means untouched. Bridges therefore forward the real
// result (pi: event.content + event.isError; opencode: the hook's output arg —
// the raw MCP CallToolResult for MCP tools) instead of synthesizing one.
function successfulResponse(response) {
  if (!response || typeof response !== 'object' || Array.isArray(response)) return false;
  if (response.isError === true || response.is_error === true || response.error || response.success === false) return false;
  if (response.status === 'error' || response.status === 'failed') return false;
  if (response.status === 'ok' || response.status === 'success') return true;
  const items = Array.isArray(response.content) ? response.content : [];
  for (const item of items) {
    if (item && (item.isError || item.is_error || item.type === 'error')) return false;
    if (item && typeof item.text === 'string') {
      try {
        const body = JSON.parse(item.text);
        if (body && (body.error || body.isError || body.is_error || body.success === false)) return false;
      } catch { /* prose is a valid successful tool result */ }
    }
  }
  // Non-empty, error-free content is the tool's actual successful result.
  if (items.length > 0) return true;
  const structured = response.structuredContent;
  if (structured && typeof structured === 'object' && !Array.isArray(structured) && Object.keys(structured).length > 0) return true;
  const result = response.result;
  if (result !== undefined && result !== null && result !== '' && result !== false) return true;
  return response.success === true;
}

// Pure: a failed, missing or ambiguous tool result cannot change the cache.
function applyEvent(chain, toolName, toolInput, toolResponse) {
  if (!successfulResponse(toolResponse)) return chain;
  const name = stripServerPrefix(toolName);
  switch (name) {
    case 'pf_update_step': {
      if (toolInput.heartbeat === true) return chain;
      const station = mapStep(toolInput.step_id);
      const next = toolInput.status === 'completed' && toolInput.next_step ? toolInput.next_step : null;
      const nextStation = next && mapStep(next);
      const advance = (state) => {
        if (!next) return state;
        if (nextStation === 'execute') return { ...state, active: 'execute', exec: { ...(state.exec || { done: [] }), active: next } };
        return { ...state, active: nextStation };
      };
      // `execute` is a multi-step phase. Track per-step sub-progress in chain.exec and
      // keep the execute station ACTIVE until the attempt wraps — do NOT mark it
      // completed on the first execute step (that collapsed N steps into one green dot).
      if (station === 'execute') {
        const exec = chain.exec || { done: [], active: null };
        if (toolInput.status === 'in_progress') {
          return { ...chain, active: 'execute', exec: { done: exec.done, active: toolInput.step_id } };
        }
        if (toolInput.status === 'completed') {
          return advance({ ...chain, active: 'execute', exec: { done: addUniq(exec.done, toolInput.step_id), active: null } });
        }
        if (toolInput.status === 'failed') return { ...chain, exec: { ...exec, active: null } };
        return chain;
      }
      // spec / plan are single-step stations.
      if (toolInput.status === 'in_progress') return { ...chain, active: station };
      if (toolInput.status === 'completed') {
        return advance({
          ...chain,
          completed: addUniq(chain.completed, station),
          active: chain.active === station ? null : chain.active,
        });
      }
      if (toolInput.status === 'failed') return { ...chain, active: chain.active === station ? null : chain.active };
      return chain;
    }
    case 'pf_save_artifact': {
      if (toolInput.type === 'spec' || toolInput.type === 'methodology.spec') return { ...chain, completed: addUniq(chain.completed, 'spec') };
      if (toolInput.type === 'methodology.plan') return { ...chain, completed: addUniq(chain.completed, 'plan') };
      return chain;
    }
    case 'pf_pause_attempt':
      return { ...chain, status: 'paused', active: null, exec: { ...(chain.exec || { done: [] }), active: null } };
    case 'pf_complete_attempt':
    case 'pf_wrap': {
      if (toolInput.status === 'paused') return { ...chain, status: 'paused', active: null, exec: { ...(chain.exec || { done: [] }), active: null } };
      if (toolInput.status === 'wrapped' || toolInput.status === 'failed' || name === 'pf_wrap') return null;
      return chain;
    }
    default:
      return chain;
  }
}

// ─── I/O wrapper ───
function readJSON(p) { try { return JSON.parse(fs.readFileSync(p, 'utf-8')); } catch { return null; } }

// Resolve the exact WI named by the call. A recent claim from another WI is
// never a fallback; a terminal call may run after its credential was deleted.
// A slug maps only when exactly one local credential state names it: taking the
// first of duplicate/corrupt state files would let an event for one WI mutate a
// different WI's display cache.
function resolveWiId(shortName, toolInput, activeState, states = []) {
  const requested = toolInput && toolInput.work_item_id;
  if (!requested) return null;
  const matches = states.filter((s) => s && (s.wi_id === requested || s.slug === requested));
  if (matches.length === 1) return matches[0].wi_id;
  if (matches.length > 1) return null;
  if (activeState && (activeState.wi_id === requested || activeState.slug === requested)) return activeState.wi_id;
  return requested.startsWith('wi_') ? requested : null;
}

function main() {
  let raw = '';
  try { raw = fs.readFileSync(0, 'utf-8'); } catch {}
  let evt = {};
  try { evt = JSON.parse(raw); } catch {}
  const toolName = evt.tool_name || evt.toolName || '';
  let toolInput = evt.tool_input || evt.toolInput;
  if (toolInput == null && typeof evt.toolArgs === 'string') {
    // Copilot CLI delivers MCP tool args as a JSON-encoded string under `toolArgs`.
    try { toolInput = JSON.parse(evt.toolArgs || '{}'); } catch { toolInput = {}; }
  }
  toolInput = toolInput || {};
  const toolResponse = evt.tool_response || evt.toolResponse;
  if (!successfulResponse(toolResponse)) return;
  const cwd = evt.cwd || process.cwd();
  const dir = path.join(cwd, '.polyforge', 'state');

  const shortName = stripServerPrefix(toolName);
  let states = [];
  try {
    states = fs.readdirSync(dir).filter((f) => f.endsWith('.json') && !f.endsWith('.chain.json'))
      .map((f) => readJSON(path.join(dir, f))).filter(Boolean);
  } catch {}
  let wiId = resolveWiId(shortName, toolInput, null, states);
  if (!wiId && toolInput.work_item_id) {
    // Completion can delete the credential before this hook runs. Match only
    // a sidecar whose stored slug equals the requested slug.
    let files = [];
    try { files = fs.readdirSync(dir).filter((f) => f.endsWith('.chain.json')); } catch {}
    const matches = files.filter((f) => readJSON(path.join(dir, f))?.wi === toolInput.work_item_id);
    if (matches.length === 1) wiId = matches[0].slice(0, -'.chain.json'.length);
  }
  if (!wiId) return;
  const st = states.find((s) => s.wi_id === wiId) || null;
  const chainPath = path.join(dir, wiId + '.chain.json');

  let chain = readJSON(chainPath);
  if (shortName === 'pf_claim_work_item') {
    if (!chain) {
      const worktree = st && st.wi_id === wiId && st.worktrees ? Object.values(st.worktrees)[0] : '';
      chain = {
        wi: (st && st.wi_id === wiId && st.slug) || wiId,
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
module.exports = { applyEvent, mapStep, resolveWiId, successfulResponse };
