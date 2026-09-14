"use strict";

/**
 * polyforge hook bridge for the opencode coding agent (@opencode-ai/plugin, sst/opencode).
 *
 * opencode has neither a hooks.json mechanism (Claude Code / Copilot) nor a
 * pi.on(event, handler) subscription API (pi). Its Plugin type
 * (@opencode-ai/plugin, dist/index.d.ts) is:
 *
 *     type Plugin = (input: PluginInput, options?: PluginOptions) => Promise<Hooks>
 *
 * i.e. a single async factory function, called once per opencode instance, that returns
 * ONE fixed `Hooks` object literal keyed by hook name ("tool.execute.before",
 * "tool.execute.after", "chat.message", "event", "permission.ask", ...). There is no way to
 * register a variable NUMBER of handlers for one hook name -- so, exactly as in the pi
 * bridge, the open-ended part (WHICH tool names route to WHICH script) is pushed into data
 * (opencode-hooks.json, a sibling of this file) and the two hook functions below loop over
 * it, rather than opencode itself offering an open-ended registration surface.
 *
 * The hook scripts are NOT modified for opencode. They are shared with the other three
 * runtimes, and the whole point of this file is that the shape conversion happens here
 * rather than as a fourth (fifth) branch inside each of them.
 *
 * ── Scope decision: only tool.execute.before / tool.execute.after are wired ──
 * pi's own bridge additionally wires a session_start-equivalent (context injection) and an
 * input-equivalent (skill routing). opencode's Hooks interface has no clean counterpart to
 * either, so both are deliberately deferred rather than approximated with a low-confidence
 * guess:
 *   - session_start: the closest thing is the generic `event` hook, which fires for EVERY
 *     server-sent event (session.created, message.updated, permission.updated, ...) with no
 *     dedicated "session start" hook name of its own. A plugin CAN filter
 *     `input.event.type === "session.created"` (confirmed to exist in @opencode-ai/sdk's
 *     Event union), but that event's firing granularity (once per session -- and opencode
 *     creates a session per subagent dispatch too, not just once per process) was not
 *     empirically characterized in this work item, so wiring pf-session-start to it risks
 *     either silent under-firing or noisy re-firing that was never measured.
 *   - input/skill-router: pi's `input` event carries the user's literal prompt text, which
 *     pf-skill-router matches a `/pf-*` slash-invocation against. opencode's Hooks interface
 *     has no equivalent "user submitted this text" hook -- `chat.message` fires for every
 *     message (not just skill invocations) and `command.execute.before` is for opencode's
 *     own distinct built-in "commands" feature, neither of which is a drop-in match.
 * Both are left to a follow-up work item. What IS wired below -- the IR1 write gate and
 * chain-state tracking -- are the two hooks this work item's non-negotiable constraints
 * name explicitly ("hooks 接线") and the two whose absence would be a silent safety gap
 * rather than a missing convenience.
 *
 * ── The throw-to-block mechanism (VERIFIED against opencode's own source, aihub#653) ──
 * @opencode-ai/plugin's own type for tool.execute.before is
 *     (input: {tool, sessionID, callID}, output: {args: any}) => Promise<void>
 * -- there is no boolean/enum field in `output` for "block this call". Throwing an Error
 * from inside the hook is opencode's documented convention for aborting a tool call from a
 * "before" hook. This work item's own live probing (chat round-trips, an `opencode serve` +
 * curl session against a real MCP server) was inconclusive three times in a row (timeouts /
 * no output, not a negative result) -- but this wi's code_review step settled it statically
 * by reading opencode 1.18.30's own compiled source rather than leaving it as a documented-
 * but-unverified convention:
 *   - packages/opencode/src/plugin/index.ts:107-122 -- `Plugin.trigger` runs
 *     `await fn(input, output)` for each registered hook in a bare loop with NO try/catch, so
 *     a throw propagates out of `trigger` itself.
 *   - packages/opencode/src/session/prompt.ts:~801 (built-in tools) and :~845 (MCP tools) --
 *     both call sites `await Plugin.trigger("tool.execute.before", ...)` BEFORE calling
 *     `execute(...)` on the tool, so a throw there is reached before the tool ever runs.
 * A throw from this bridge does therefore prevent the tool call, confirmed from the runtime's
 * own control flow, not inferred from documentation alone.
 *
 * ── Why CommonJS ──
 * Matches pi/extensions/polyforge/index.js's own reasoning: plain CJS lets `node --test`
 * require this file directly with no transpiler, which is what makes the gate logic below
 * testable on any runner. `createBridge` is exported for exactly that.
 */

const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const CONFIG_BASENAME = "polyforge-opencode.json";
const HOOKS_BASENAME = "opencode-hooks.json";
// aihub#653's file_scope lock covers exactly plugins/polyforge/opencode, so opencode-hooks.json
// is nested ONE level under the true plugin root instead of living as a plugin-root sibling
// the way pi-hooks.json/codex-hooks.json do (see opencode-hooks.json's own _comment for why).
// Every function below that needs to READ that file must go through this path; every OTHER
// use of "pluginRoot" in this file means the TRUE root (plugins/polyforge), because that is
// what hookEnv() hands the shared hook scripts as CLAUDE_PLUGIN_ROOT so they can find their
// OWN plugins/polyforge/hooks/ and plugins/polyforge/bin/ -- those are not under opencode/.
const HOOKS_SUBDIR = "opencode";
function hooksJsonPath(root) {
	return path.join(root, HOOKS_SUBDIR, HOOKS_BASENAME);
}

// ─────────────────────────── discovery ───────────────────────────

/**
 * Absolute path of plugins/polyforge/ (the TRUE plugin root -- see the constants above for
 * why that is not the same directory opencode-hooks.json lives in).
 *
 * install.sh COPIES this file to ~/.config/opencode/plugin/polyforge-hooks.js (opencode's
 * global auto-load plugin directory -- aihub#653 measurement: any .js file dropped there is
 * loaded with no config entry needed at all), so __dirname does not sit inside the plugin
 * checkout at runtime. Three sources, most explicit first; each is independently
 * sufficient. Mirrors pi/extensions/polyforge/index.js's resolvePluginRoot in spirit (same
 * three-tier fallback), adapted for the one-level-nested hooks file.
 */
function resolvePluginRoot() {
	const fromEnv = process.env.POLYFORGE_PLUGIN_ROOT;
	if (fromEnv && fs.existsSync(hooksJsonPath(fromEnv))) return fromEnv;

	// install.sh writes this next to the copied plugin file, recording where it came from.
	try {
		const cfg = JSON.parse(fs.readFileSync(path.join(__dirname, CONFIG_BASENAME), "utf-8"));
		if (cfg && typeof cfg.pluginRoot === "string" && fs.existsSync(hooksJsonPath(cfg.pluginRoot))) {
			return cfg.pluginRoot;
		}
	} catch {
		/* absent or unreadable -> fall through */
	}

	// Running in place from the checkout (a relative "plugin" config entry pointing directly
	// at plugins/polyforge/opencode/plugin/polyforge-hooks.js, no install.sh copy involved).
	// plugin.json lives at the TRUE root (a sibling of opencode/, not inside it), so the two
	// existence checks below are deliberately against DIFFERENT paths relative to `cur` -- an
	// earlier version of this function checked both against `cur` itself, which can never be
	// true simultaneously (opencode-hooks.json and plugin.json are never in the same directory)
	// and silently made this fallback tier dead code. Caught in review before this shipped.
	let cur = __dirname;
	for (let i = 0; i < 8; i++) {
		if (fs.existsSync(hooksJsonPath(cur)) && fs.existsSync(path.join(cur, "plugin.json"))) return cur;
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
		const doc = JSON.parse(fs.readFileSync(hooksJsonPath(pluginRoot), "utf-8"));
		const hooks = doc && doc.hooks;
		return hooks && typeof hooks === "object" ? hooks : {};
	} catch {
		return {};
	}
}

// ─────────────────────────── execution ───────────────────────────

/**
 * Run one hook script with `payload` on stdin. Identical contract to
 * pi/extensions/polyforge/index.js's runHook: FAIL-OPEN on every failure mode (spawn error,
 * non-zero exit, timeout, unparseable output) -- callers treat a failed result as "no
 * decision", matching pf-commit-guard's own documented fail-open behaviour.
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

		// See pi's bridge for why this listener exists: an EPIPE on stdin arrives as an
		// asynchronous 'error' event that an unhandled listener would otherwise let bubble up
		// and terminate the host process.
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
 * Build the Hooks object opencode's Plugin factory returns.
 *
 * Separated from the factory entry point so a test can supply its own plugin root and hook
 * table and drive the real handlers against the real hook scripts, without a running
 * opencode instance -- same seam pi/extensions/polyforge/index.js's createBridge provides.
 *
 * @param {{pluginRoot: string, hooks: object, directory?: string, worktree?: string, exec?: Function}} opts
 *        `exec` defaults to runHook and exists so a test can assert on what would be run.
 * @returns {import("@opencode-ai/plugin").Hooks}
 */
function createBridge(opts) {
	const pluginRoot = opts.pluginRoot;
	const hooks = opts.hooks || {};
	const exec = opts.exec || runHook;
	const cwd = opts.directory || opts.worktree || process.cwd();
	const workspaceRoot = findWorkspaceRoot(cwd);

	/**
	 * Environment for every hook invocation. Neither PLUGIN_ROOT nor CURSOR_PLUGIN_ROOT is
	 * set -- see pi/extensions/polyforge/index.js's file header for why that distinction is
	 * load-bearing for pf-session-start (not used by this bridge today, but the hook scripts
	 * this bridge DOES call are shared code, so the same care applies in case that changes).
	 */
	function hookEnv() {
		const env = Object.assign({}, process.env);
		env.CLAUDE_PLUGIN_ROOT = pluginRoot;
		if (workspaceRoot) env.CLAUDE_PROJECT_DIR = workspaceRoot;
		delete env.PLUGIN_ROOT;
		delete env.CURSOR_PLUGIN_ROOT;
		env.POLYFORGE_RUNTIME = "opencode";
		return env;
	}

	function entriesFor(event) {
		const list = hooks[event];
		if (!Array.isArray(list)) return [];
		return list.filter((e) => e && typeof e.bash === "string");
	}

	function matchEntry(entry, subject, kind) {
		if ((entry.match == null ? kind : entry.match) !== kind) return null;
		if (!entry.matcher) return [subject];
		try {
			return subject.match(new RegExp(entry.matcher));
		} catch {
			return null; // a bad matcher disables its own entry, it does not crash the session
		}
	}

	return {
		// ── tool.execute.before -> pf-commit-guard (IR1 write gate) ──
		//
		// "before", not "after": a block returned (thrown) after execution is already too
		// late -- matching pi's own tool_call-not-tool_execution_start choice (aihub#503).
		"tool.execute.before": async (input, output) => {
			const toolName = (input && input.tool) || "";
			if (!toolName) return;
			for (const entry of entriesFor("tool.execute.before")) {
				if (!matchEntry(entry, toolName, "toolName")) continue;
				const payload = JSON.stringify({
					tool_name: toolName,
					tool_input: (output && output.args) || {},
					cwd,
				});
				const res = await exec(entry.bash, payload, hookEnv(), entry.timeoutSec || 10);
				if (entry.inject === "none") continue;
				const reason = extractDeny(parseJSON(res.stdout));
				if (reason) {
					// See the file header: throwing here is opencode's documented (but, in this
					// work item, NOT independently live-verified) mechanism for a "before" hook
					// to abort the tool call.
					throw new Error(`[polyforge IR1] BLOCKED: ${reason}`);
				}
			}
		},

		// ── tool.execute.after -> pf-chain-hook.cjs (wi lifecycle chain tracking) ──
		"tool.execute.after": async (input) => {
			const toolName = (input && input.tool) || "";
			if (!toolName || !workspaceRoot) return;
			for (const entry of entriesFor("tool.execute.after")) {
				if (!matchEntry(entry, toolName, "toolName")) continue;
				const payload = JSON.stringify({
					tool_name: toolName,
					tool_input: (input && input.args) || {},
					tool_response: {},
					cwd: workspaceRoot,
				});
				await exec(entry.bash, payload, hookEnv(), entry.timeoutSec || 10);
			}
		},
	};
}

// ─────────────────────────── entry point ───────────────────────────

/** @type {import("@opencode-ai/plugin").Plugin} */
async function polyforgeHooksPlugin(input) {
	// Going inert is the right answer for a convenience hook and the WORST one for a write
	// gate: everything looks normal and IR1 is simply gone. So the two ways that happens are
	// announced rather than swallowed -- same reasoning as pi's bridge. Reachable in
	// practice: this file is a COPY in ~/.config/opencode/plugin/ while the hook scripts stay
	// in the checkout, so moving or renaming the checkout after install.sh ran breaks the
	// recorded path, as does a hand edit that leaves opencode-hooks.json unparseable.
	const pluginRoot = resolvePluginRoot();
	if (!pluginRoot) {
		console.error(
			"[polyforge] hook bridge INERT: cannot locate the plugin checkout, so the IR1 write " +
				"gate is NOT running. Set POLYFORGE_PLUGIN_ROOT, or re-run plugins/polyforge/opencode/install.sh.",
		);
		return {};
	}

	const hooks = loadHookConfig(pluginRoot);
	if (!Object.keys(hooks).length) {
		console.error(
			`[polyforge] hook bridge INERT: ${hooksJsonPath(pluginRoot)} is missing, ` +
				"unreadable or has no hooks, so the IR1 write gate is NOT running.",
		);
		return {};
	}

	return createBridge({
		pluginRoot,
		hooks,
		directory: input && input.directory,
		worktree: input && input.worktree,
	});
}

module.exports = polyforgeHooksPlugin;
module.exports.default = polyforgeHooksPlugin;
module.exports.createBridge = createBridge;
module.exports.loadHookConfig = loadHookConfig;
module.exports.findWorkspaceRoot = findWorkspaceRoot;
module.exports.resolvePluginRoot = resolvePluginRoot;
module.exports.extractDeny = extractDeny;
module.exports.parseJSON = parseJSON;
module.exports.runHook = runHook;
module.exports.hooksJsonPath = hooksJsonPath;
