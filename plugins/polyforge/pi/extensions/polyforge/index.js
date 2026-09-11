"use strict";

/**
 * polyforge hook bridge for the pi coding agent (@earendil-works/pi-coding-agent).
 *
 * pi has no hooks.json mechanism — it exposes a TypeScript extension API. This bridge is
 * the adapter between the two: it subscribes to pi events and converts each one into the
 * payload shape polyforge's existing hook scripts already read on stdin, then converts
 * their output back into pi's return protocol.
 *
 * The hook scripts are NOT modified for pi. They are shared with the other three runtimes,
 * and the whole point of this file is that the shape conversion happens here rather than
 * as a fourth branch inside each of them.
 *
 * Registration is data, not code: `pi-hooks.json` at the plugin root lists which pi event
 * drives which script, mirroring hooks.json / codex-hooks.json / copilot-hooks.json. That
 * is deliberate — tests/pf-commit-guard.test.sh asserts over those files, and a matcher
 * hardcoded here would be invisible to that gate.
 *
 * ── Why CommonJS rather than the .ts the pi examples use ──
 * pi's loader accepts `index.ts` OR `index.js` (core/extensions/loader.js) and runs either
 * through jiti, so both load identically. Plain CJS additionally lets `node --test` require
 * this file with no transpiler and no version-gated flag, which is what makes the gate
 * logic below testable on any runner. `createBridge` is exported for exactly that: it takes
 * the plugin root and the hook table as arguments so a test can drive the real handlers
 * against the real hook scripts.
 *
 * ── Runtime identification (load-bearing, verified by reading the scripts) ──
 * pf-session-start picks its output shape purely from environment variables:
 *     if   os.environ.get("CURSOR_PLUGIN_ROOT"):  -> Cursor shape
 *     elif os.environ.get("PLUGIN_ROOT"):         -> Codex shape (nested key only)
 *     else:                                       -> Claude Code / Copilot shape (both keys)
 * So this bridge MUST NOT export a bare PLUGIN_ROOT: pi would be misread as Codex and the
 * top-level `additionalContext` and `systemMessage` would silently vanish. It exports
 * CLAUDE_PLUGIN_ROOT (which that script deliberately ignores, and pf-skill-router uses)
 * and CLAUDE_PROJECT_DIR (pf-session-start's only workspace entry point).
 */

const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const CONFIG_BASENAME = "polyforge-pi.json";
const HOOKS_BASENAME = "pi-hooks.json";
const STATUS_KEY = "polyforge";

// ─────────────────────────── discovery ───────────────────────────

/**
 * Absolute path of plugins/polyforge/.
 *
 * The extension is COPIED to ~/.pi/agent/extensions/polyforge/ by install.sh (pi's package
 * mechanism cannot carry it, and project-local extension dirs are a deliberate supply-chain
 * gate), so __dirname does not sit inside the plugin checkout. Three sources, most explicit
 * first; each is independently sufficient.
 */
function resolvePluginRoot() {
	const fromEnv = process.env.POLYFORGE_PLUGIN_ROOT;
	if (fromEnv && fs.existsSync(path.join(fromEnv, HOOKS_BASENAME))) return fromEnv;

	// install.sh writes this next to the copied extension, recording where it came from.
	try {
		const cfg = JSON.parse(fs.readFileSync(path.join(__dirname, CONFIG_BASENAME), "utf-8"));
		if (cfg && typeof cfg.pluginRoot === "string" && fs.existsSync(path.join(cfg.pluginRoot, HOOKS_BASENAME))) {
			return cfg.pluginRoot;
		}
	} catch {
		/* absent or unreadable -> fall through */
	}

	// Running in place from the checkout (pi -e plugins/polyforge/pi/extensions/polyforge).
	let cur = __dirname;
	for (let i = 0; i < 8; i++) {
		if (fs.existsSync(path.join(cur, HOOKS_BASENAME)) && fs.existsSync(path.join(cur, "plugin.json"))) return cur;
		const parent = path.dirname(cur);
		if (parent === cur) break;
		cur = parent;
	}
	return null;
}

/** Nearest ancestor of `start` holding .polyforge.yaml. */
function findWorkspaceRoot(start) {
	let cur;
	try {
		cur = path.resolve(start);
	} catch {
		return null;
	}
	for (let i = 0; i < 64; i++) {
		if (fs.existsSync(path.join(cur, ".polyforge.yaml"))) return cur;
		const parent = path.dirname(cur);
		if (parent === cur) return null;
		cur = parent;
	}
	return null;
}

function loadHookConfig(pluginRoot) {
	try {
		const doc = JSON.parse(fs.readFileSync(path.join(pluginRoot, HOOKS_BASENAME), "utf-8"));
		const hooks = doc && doc.hooks;
		return hooks && typeof hooks === "object" ? hooks : {};
	} catch {
		return {};
	}
}

// ─────────────────────────── execution ───────────────────────────

/**
 * Run one hook script with `payload` on stdin.
 *
 * FAIL-OPEN by construction: every failure mode (spawn error, non-zero exit, timeout,
 * unparseable output) resolves rather than rejects, and callers treat a failed result as
 * "no decision". That matches the scripts' own contract — pf-commit-guard's header states
 * it exits 0 on any internal error so a guard bug can never wedge real work — and keeping
 * the bridge's behaviour identical is the point of the bridge.
 */
function runHook(command, payload, env, timeoutSec) {
	return new Promise((resolve) => {
		let child;
		try {
			child = spawn(command, { shell: true, env, stdio: ["pipe", "pipe", "pipe"] });
		} catch {
			resolve({ stdout: "", stderr: "", timedOut: false, failed: true });
			return;
		}

		let stdout = "";
		let stderr = "";
		let settled = false;
		const finish = (r) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			resolve(r);
		};

		const timer = setTimeout(
			() => {
				try {
					child.kill("SIGKILL");
				} catch {
					/* already gone */
				}
				finish({ stdout, stderr, timedOut: true, failed: true });
			},
			Math.max(1, timeoutSec) * 1000,
		);

		if (child.stdout) child.stdout.on("data", (d) => (stdout += String(d)));
		if (child.stderr) child.stderr.on("data", (d) => (stderr += String(d)));
		child.on("error", () => finish({ stdout, stderr, timedOut: false, failed: true }));
		child.on("close", (code) => finish({ stdout, stderr, timedOut: false, failed: code !== 0 }));

		// EPIPE on stdin arrives as an ASYNCHRONOUS 'error' event, which the try/catch below
		// cannot see, and an unhandled 'error' event terminates the host process — the one
		// failure mode this whole function is written to avoid (pi installs no
		// uncaughtException handler). A hook that exits before reading its payload is an
		// ordinary outcome (pf-session-start reads no stdin at all), so this is swallowed
		// rather than reported; `close` still resolves the promise.
		if (child.stdin) child.stdin.on("error", () => {});

		try {
			if (child.stdin) child.stdin.end(payload);
		} catch {
			/* the close/error handler resolves */
		}
	});
}

function parseJSON(text) {
	try {
		const t = String(text || "").trim();
		return t ? JSON.parse(t) : null;
	} catch {
		return null;
	}
}

/**
 * The context a hook wants injected.
 *
 * pf-session-start and pf-skill-router both emit the dual shape — a top-level
 * `additionalContext` for Copilot CLI and a nested hookSpecificOutput.additionalContext for
 * Claude Code — precisely so one script serves several harnesses. Read either.
 */
function extractContext(out) {
	if (!out || typeof out !== "object") return null;
	const nested = out.hookSpecificOutput && out.hookSpecificOutput.additionalContext;
	const ctx = nested || out.additionalContext || out.additional_context;
	return typeof ctx === "string" && ctx.trim() ? ctx : null;
}

/** A deny decision, in either of the two shapes pf-commit-guard emits. */
function extractDeny(out) {
	if (!out || typeof out !== "object") return null;
	const nested = out.hookSpecificOutput || {};
	if ((out.permissionDecision || nested.permissionDecision) !== "deny") return null;
	const reason = out.permissionDecisionReason || nested.permissionDecisionReason;
	return typeof reason === "string" && reason ? reason : "blocked by polyforge hook";
}

// ─────────────────────────── the bridge ───────────────────────────

/**
 * Subscribe the handlers to `pi`.
 *
 * Separated from the extension entry point so a test can supply its own `pi` (capturing the
 * handlers), its own plugin root, and its own hook table, and then drive the real handlers
 * against the real hook scripts. Without this seam the gate logic below is only reachable
 * through a running pi, and "the handler stopped returning {block:true}" is invisible.
 *
 * @param {{on: Function}} pi
 * @param {{pluginRoot: string, hooks: object, exec?: Function}} opts
 *        `exec` defaults to runHook and exists so a test can assert on what would be run.
 */
function createBridge(pi, opts) {
	const pluginRoot = opts.pluginRoot;
	const hooks = opts.hooks || {};
	const exec = opts.exec || runHook;

	// Context produced by session_start / input, drained into the next agent turn.
	const pending = [];
	let notices = [];

	/**
	 * Environment for every hook invocation.
	 *
	 * `workspaceRoot` is passed as CLAUDE_PROJECT_DIR because that is pf-session-start's
	 * only workspace entry point (it reads no stdin at all). Neither PLUGIN_ROOT nor
	 * CURSOR_PLUGIN_ROOT is set — see the runtime-identification note in the file header.
	 */
	function hookEnv(workspaceRoot) {
		const env = Object.assign({}, process.env);
		env.CLAUDE_PLUGIN_ROOT = pluginRoot;
		if (workspaceRoot) env.CLAUDE_PROJECT_DIR = workspaceRoot;
		delete env.PLUGIN_ROOT;
		delete env.CURSOR_PLUGIN_ROOT;
		env.POLYFORGE_RUNTIME = "pi";
		return env;
	}

	function entriesFor(event) {
		const list = hooks[event];
		if (!Array.isArray(list)) return [];
		return list.filter((e) => e && (typeof e.bash === "string" || typeof e.deny === "string"));
	}

	/**
	 * `kind` is what THIS event offers as the match subject. An entry declaring a different
	 * `match` is skipped rather than quietly matched against the wrong string: an entry that
	 * says `match: "text"` sitting under `tool_call` is a configuration mistake, and silently
	 * testing its pattern against a tool name is how such a mistake produces a rule that
	 * never fires and never complains.
	 */
	function matchEntry(entry, subject, kind) {
		if ((entry.match == null ? kind : entry.match) !== kind) return null;
		if (kind === "always" || !entry.matcher) return [subject];
		try {
			return subject.match(new RegExp(entry.matcher));
		} catch {
			return null; // a bad matcher disables its own entry, it does not crash the session
		}
	}

	// ── session_start -> pf-session-start ──────────────────────────
	pi.on("session_start", async (_event, ctx) => {
		const ws = findWorkspaceRoot((ctx && ctx.cwd) || process.cwd());
		for (const entry of entriesFor("session_start")) {
			if (!matchEntry(entry, "", "always") || !entry.bash) continue;
			// This script reads NOTHING from stdin — everything arrives via env.
			const res = await exec(entry.bash, "", hookEnv(ws), entry.timeoutSec || 15);
			const out = parseJSON(res.stdout);
			const context = extractContext(out);
			if (context && entry.inject !== "none") pending.push(context);
			if (out && typeof out.systemMessage === "string" && out.systemMessage.trim()) {
				notices.push(out.systemMessage);
			}
		}
		if (pending.length && ctx && ctx.ui && typeof ctx.ui.setStatus === "function") {
			ctx.ui.setStatus(STATUS_KEY, "polyforge");
		}
	});

	// ── input -> pf-skill-router ───────────────────────────────────
	//
	// In Claude Code this hook is a PreToolUse on the `Skill` tool. pi has no such tool —
	// skills are expanded from the prompt — so the equivalent trigger is the user's input
	// text, and the matcher's first capture group supplies the skill name. The script reads
	// exactly one routing key, tool_input.skill, and never looks at tool_name, so a payload
	// carrying just that key is a complete request as far as it is concerned.
	pi.on("input", async (event, ctx) => {
		const text = event && typeof event.text === "string" ? event.text : "";
		if (!text) return undefined;
		const cwd = (ctx && ctx.cwd) || process.cwd();
		const ws = findWorkspaceRoot(cwd);
		for (const entry of entriesFor("input")) {
			const m = matchEntry(entry, text, "text");
			if (!m) continue;
			const skill = String(m[1] || "").trim();
			if (!skill || !entry.bash) continue;
			const payload = JSON.stringify({ tool_input: { skill }, cwd });
			const res = await exec(entry.bash, payload, hookEnv(ws), entry.timeoutSec || 10);
			const context = extractContext(parseJSON(res.stdout));
			if (context && entry.inject !== "none") pending.push(context);
		}
		return undefined; // never transform or swallow the user's own input
	});

	// ── drain: injected context reaches the model here ─────────────
	//
	// Claude Code's additionalContext lands in the turn the hook fired on. pi's equivalent
	// is a message returned from before_agent_start, which is why session_start and input
	// queue rather than inject directly: neither of those handlers can return a message.
	pi.on("before_agent_start", async (_event, ctx) => {
		for (const n of notices) {
			try {
				if (ctx && ctx.ui && ctx.ui.notify) ctx.ui.notify(n, "warning");
			} catch {
				/* headless -> no UI to notify */
			}
		}
		notices = [];
		if (!pending.length) return undefined;
		const content = pending.join("\n\n---\n\n");
		pending.length = 0;
		// `display` is a BOOLEAN (core/messages.d.ts: `display: boolean`), not a mode string.
		// false = carried in the conversation but not rendered in the transcript, which is
		// what Claude Code's additionalContext does. A string here would be truthy and the
		// whole payload would be printed at the user.
		return { message: { customType: "polyforge-context", content, display: false } };
	});

	// ── tool_call -> pf-commit-guard (IR1 write gate) ──────────────
	//
	// tool_call, NOT tool_execution_start: the latter fires AFTER the gate point, so a block
	// returned there is already too late (measured, aihub#503).
	pi.on("tool_call", async (event, ctx) => {
		const toolName = event && typeof event.toolName === "string" ? event.toolName : "";
		if (!toolName) return undefined;
		const cwd = (ctx && ctx.cwd) || process.cwd();
		const ws = findWorkspaceRoot(cwd);
		for (const entry of entriesFor("tool_call")) {
			if (!matchEntry(entry, toolName, "toolName")) continue;
			// A `deny` entry refuses outright, running nothing. That is what closes the IR1
			// bypass doors (`mcp`, `mcpScript`): .mcp.json's settings can fail to hide them —
			// on a cold metadata cache the adapter registers the proxy tool even with
			// disableProxyTool:true — so the block cannot be delegated to configuration.
			if (typeof entry.deny === "string" && entry.deny) return { block: true, reason: entry.deny };
			if (!entry.bash) continue;
			// `cwd` is what the guard's worktree rule (1a) and finishing-ops rule (1c) resolve
			// paths against; pi's event carries no cwd of its own, so it comes from ctx.
			const payload = JSON.stringify({ tool_name: toolName, tool_input: (event && event.input) || {}, cwd });
			const res = await exec(entry.bash, payload, hookEnv(ws), entry.timeoutSec || 10);
			if (entry.inject === "none") continue;
			const reason = extractDeny(parseJSON(res.stdout));
			if (reason) return { block: true, reason };
		}
		return undefined;
	});

	// ── tool_result -> pf-chain-hook.cjs ───────────────────────────
	pi.on("tool_result", async (event, ctx) => {
		const toolName = event && typeof event.toolName === "string" ? event.toolName : "";
		if (!toolName) return undefined;
		const ws = findWorkspaceRoot((ctx && ctx.cwd) || process.cwd());
		for (const entry of entriesFor("tool_result")) {
			if (!matchEntry(entry, toolName, "toolName")) continue;
			// ⚠️ This consumer does NOT walk up for .polyforge.yaml — pf-chain-hook.cjs joins
			// `cwd` with .polyforge/state directly, so cwd must BE the workspace root. Passing
			// ctx.cwd (typically a pf.<slug>/<repo>/ worktree) makes it read a directory that
			// does not exist, return null, and exit 0 having written nothing.
			if (!ws || !entry.bash) continue;
			const payload = JSON.stringify({
				tool_name: toolName,
				tool_input: (event && event.input) || {},
				tool_response: { isError: Boolean(event && event.isError) },
				cwd: ws,
			});
			await exec(entry.bash, payload, hookEnv(ws), entry.timeoutSec || 10);
		}
		return undefined;
	});
}

// ─────────────────────────── entry point ───────────────────────────

function extension(pi) {
	// Going inert is the right answer for a convenience hook and the WORST one for a write
	// gate: everything looks normal and IR1 is simply gone. So the two ways that happens are
	// announced rather than swallowed. Reachable in practice — the extension is a COPY in
	// ~/.pi/agent/extensions/ while the hook scripts stay in the checkout, so moving or
	// renaming the checkout after install.sh ran breaks the recorded path, as does a hand
	// edit that leaves pi-hooks.json unparseable.
	const pluginRoot = resolvePluginRoot();
	if (!pluginRoot) {
		console.error(
			"[polyforge] hook bridge INERT: cannot locate the plugin checkout, so the IR1 write " +
				"gate is NOT running. Set POLYFORGE_PLUGIN_ROOT, or re-run plugins/polyforge/pi/install.sh.",
		);
		return;
	}

	const hooks = loadHookConfig(pluginRoot);
	if (!Object.keys(hooks).length) {
		console.error(
			`[polyforge] hook bridge INERT: ${path.join(pluginRoot, HOOKS_BASENAME)} is missing, ` +
				"unreadable or has no hooks, so the IR1 write gate is NOT running.",
		);
		return;
	}

	createBridge(pi, { pluginRoot, hooks });
}

module.exports = extension;
// jiti resolves `{ default: true }` against module.exports; naming it explicitly keeps the
// ESM-interop path unambiguous whichever loader picks this file up.
module.exports.default = extension;
module.exports.createBridge = createBridge;
module.exports.loadHookConfig = loadHookConfig;
module.exports.findWorkspaceRoot = findWorkspaceRoot;
module.exports.resolvePluginRoot = resolvePluginRoot;
module.exports.extractContext = extractContext;
module.exports.extractDeny = extractDeny;
module.exports.parseJSON = parseJSON;
module.exports.runHook = runHook;
