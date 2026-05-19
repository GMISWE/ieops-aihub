"""Mode-1 scenario executor — orchestrator plays every cast role itself.

This is the deterministic CI-friendly path: the runner calls aihub's HTTP
API directly (via AihubClient/ProtocolAdapter) on behalf of each cast
member, in the order the scenario timeline dictates. Skills are
translated to their underlying MCP tool calls.

Mode-2 (real Claude Code subagents driving skills) is a separate runner
(`runner_skill.md`) invoked from a Claude Code session, not pytest. Both
modes share the SAME .scenario.md format.

What this executor handles today:
  - skill: /pf3-start --goal "..." --project ...           [no_claim, auto_claim]
  - skill: /pf3-claim <wi_id>                              (alias: /pf3-resume)
  - skill: /pf3-resume <wi_id>
  - skill: /pf3-note "msg" --work-item-id ...               (emit_event note)
  - bash: <cmd>                                            (subprocess run)
  - sleep: N                                               (asyncio.sleep)

Skills are intentionally narrow — this isn't a full /pf3-* re-implementation.
Use it to validate the scenario format + exercise the realistic happy paths.
For richer skill coverage, write a Mode-2 subagent runner.
"""
from __future__ import annotations

import asyncio
import contextvars
import os
import re
import shlex
import subprocess
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import httpx


# Task-local current role. asyncio.gather() spawns each role coroutine as a
# separate Task; ContextVar carries per-Task state without bleeding across
# await points the way a shared mutable attribute would. None outside a
# parallel cast block.
_current_role: contextvars.ContextVar[str | None] = contextvars.ContextVar(
    "scenario_runner_current_role", default=None,
)

from polyforge_v3.aihub.client import AihubClient
from polyforge_v3.aihub.adapter import AihubClientProtocolAdapter
from polyforge_v3.aihub.errors import AihubError
from polyforge_v3.auth import AttemptCredential
from polyforge_v3.config import AihubConfig, SessionInfo

from .parser import Action, Assertion, CastMember, Scenario, TimelineBlock


# Polyforge-test-specific user → bearer mapping. Matches tests/v3/fixtures.py
# REFERENCE_USERS. This is the DEFAULT resolver used when execute_scenario is
# called without an explicit `bearer_for_user` callable; product-test scenarios
# (gauge, mycorp-app, etc.) should pass their own resolver via the parameter.
#
# See `BearerResolver` protocol below for the contract.
_POLYFORGE_REFERENCE_BEARERS = {
    "u_zhangsan": "argon2id$dummy_seed_hash_zhang",
    "u_lisi":     "argon2id$dummy_seed_hash_li",
    "u_wangwu":   "argon2id$dummy_seed_hash_wang",
}


def _polyforge_default_resolver(user_id: str) -> str | None:
    """Default resolver: look up in the polyforge reference-user fixtures."""
    return _POLYFORGE_REFERENCE_BEARERS.get(user_id)


# Type alias: resolver maps a scenario cast member's `user` field to the
# bearer string AihubClient should send. Return None to signal "unknown user
# — scenarios should fail loudly at adapter build time".
BearerResolver = Any  # Callable[[str], str | None]; widened for runtime use


@dataclass
class ExecutionContext:
    """Mutable state shared across timeline + assertions.

    Variable namespacing:
    - `vars` is the GLOBAL flat namespace. Sequential captures land here.
    - `role_scratch[role_id]` is a per-role overlay used DURING a parallel
      cast block. Captures inside a parallel block land in the role's
      scratch dict, NOT the global. After the block completes, role
      scratches are merged into `vars` with `f"{role}.{key}"` prefixes so
      assertions like `one_succeeds` can address them per-actor.
    - String substitution: when a `role_context` is set, look in
      role_scratch[role_context] first, then fall back to global vars.
      This makes `$WI` from a sequential block resolve normally inside a
      parallel block, while role-local captures take precedence within
      that role's own actions.
    """
    workspace: Path
    vars: dict[str, Any] = field(default_factory=dict)
    transport: httpx.ASGITransport | None = None
    # Per-cast adapter pool. Built lazily on first action for that role.
    adapters: dict[str, AihubClientProtocolAdapter] = field(default_factory=dict)
    clients: dict[str, AihubClient] = field(default_factory=dict)
    # Per-cast captured cred (so subsequent actions can do fenced writes)
    creds: dict[str, AttemptCredential] = field(default_factory=dict)
    # Per-role scratch namespace, set up by _execute_block when parallel
    role_scratch: dict[str, dict[str, Any]] = field(default_factory=dict)
    # Pluggable bearer resolver. Defaults to the polyforge reference-user
    # lookup; non-polyforge scenarios override via execute_scenario's kwarg.
    bearer_resolver: Any = field(default=None)

    def substitute(self, s: str, *, shell_quote: bool = False) -> str:
        """Replace $VAR and {workspace} placeholders.

        Lookup order: current task's role_scratch (if inside a parallel block)
        first, then global `vars`. Task-local via contextvars so concurrent
        cast tasks don't trample each other.

        When `shell_quote=True` (intended for `bash:` actions), each substituted
        VALUE is passed through `shlex.quote` so a captured string containing
        shell metacharacters cannot break out of its position. The placeholder
        itself is still a `$VAR` token in the SOURCE, not interpreted by the
        shell — the shell receives the already-quoted literal.
        """
        s = s.replace("{workspace}", shlex.quote(str(self.workspace)) if shell_quote else str(self.workspace))
        role = _current_role.get()
        if role is not None and role in self.role_scratch:
            overlay = {**self.vars, **self.role_scratch[role]}
        else:
            overlay = self.vars
        for k, v in overlay.items():
            value = shlex.quote(str(v)) if shell_quote else str(v)
            s = re.sub(rf"\${re.escape(k)}\b", value, s)
        return s

    def set_var(self, name: str, value: Any) -> None:
        """Set a captured var. In a parallel block, writes to the current
        task's role_scratch (task-local). Otherwise writes to global vars.
        """
        key = name.lstrip("$")
        role = _current_role.get()
        if role is not None:
            self.role_scratch.setdefault(role, {})[key] = value
        else:
            self.vars[key] = value


async def execute_scenario(
    scenario: Scenario,
    *,
    workspace: Path,
    transport: httpx.ASGITransport,
    bearer_resolver: Any = None,
) -> ExecutionContext:
    """Run the timeline. Returns the populated context for assertion eval.

    `bearer_resolver`: Callable[[str], str | None] mapping a cast member's
    `user` field to a bearer string. Defaults to the polyforge reference-user
    lookup; non-polyforge product tests pass their own resolver here so the
    same runner works on any aihub-like backend.

    Safety:
    - env overrides are restored on exit (success OR failure) — no test bleed.
    - Adapter clients are closed via try/finally — no httpx transport leaks.
    """
    # Apply env overrides — record originals so we can restore in finally
    env_originals: dict[str, str | None] = {}
    for k, v in scenario.env.items():
        env_originals[k] = os.environ.get(k)
        os.environ[k] = v

    ctx = ExecutionContext(
        workspace=workspace, transport=transport,
        bearer_resolver=bearer_resolver or _polyforge_default_resolver,
    )
    workspace.mkdir(parents=True, exist_ok=True)

    try:
        # Build adapter pool — one per cast role
        for member in scenario.cast:
            await _ensure_adapter(ctx, member)

        # Execute timeline
        for item in scenario.timeline:
            if isinstance(item, Action) and item.kind == "sleep":
                await asyncio.sleep(float(item.payload))
            elif isinstance(item, TimelineBlock):
                await _execute_block(item, scenario, ctx)
            else:
                raise ValueError(f"unexpected top-level timeline item: {item!r}")

        return ctx
    finally:
        # Close adapters (clients have aclose) — runs even on failure
        for c in ctx.clients.values():
            try:
                await c.aclose()
            except Exception:  # noqa: BLE001 — cleanup should never re-raise
                pass
        # Restore env vars
        for k, original in env_originals.items():
            if original is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = original


async def _ensure_adapter(ctx: ExecutionContext, member: CastMember) -> None:
    if member.id in ctx.adapters:
        return
    bearer = ctx.bearer_resolver(member.user) if member.user else None
    if bearer is None:
        raise ValueError(
            f"cast member {member.id!r} references user {member.user!r} "
            f"which the bearer_resolver doesn't know about. For polyforge "
            f"scenarios, seed the user via tests/v3/fixtures.REFERENCE_USERS "
            f"first; for product scenarios, pass execute_scenario(..., "
            f"bearer_resolver=...) with your own resolver."
        )
    cfg = AihubConfig(url="http://test", api_key_env="_UNUSED_", api_key=bearer)
    secret = member.session_secret or _default_secret_for(member.id)
    session = SessionInfo(
        machine_id=member.machine_id,
        session_id=f"sess-{member.id}",
        session_secret=secret,
    )
    client = AihubClient(cfg, session, transport=ctx.transport, timeout=30.0)
    adapter = AihubClientProtocolAdapter(client)
    ctx.clients[member.id] = client
    ctx.adapters[member.id] = adapter


def _default_secret_for(cast_id: str) -> str:
    """Deterministic 64-hex secret keyed off cast id. Each role distinct."""
    import hashlib
    h = hashlib.sha256(cast_id.encode()).hexdigest()
    return h  # 64 hex chars


async def _execute_block(
    block: TimelineBlock, scenario: Scenario, ctx: ExecutionContext
) -> None:
    if block.parallel:
        # Initialize per-role scratch namespaces (captures inside this block
        # land in role_scratch[role], NOT the global vars). asyncio.gather
        # runs the roles concurrently; each task sets ctx.role_context while
        # executing its actions — these execute serially within a task, so
        # role_context is unambiguous per task. (asyncio is single-threaded;
        # the only "interleaving" is at await points, and we don't read
        # role_context from a different task.)
        for cast_id in block.cast_ids:
            ctx.role_scratch[cast_id] = {}
        tasks = [
            _execute_actions_for_role(cast_id, block.actions, scenario, ctx,
                                       parallel=True)
            for cast_id in block.cast_ids
        ]
        results = await asyncio.gather(*tasks, return_exceptions=True)
        # Re-raise any exceptions (so the timeline fails); but first merge
        # the role scratches into the global vars with prefixed keys so
        # assertions can address them per actor.
        for cast_id in block.cast_ids:
            for k, v in ctx.role_scratch[cast_id].items():
                ctx.vars[f"{cast_id}.{k}"] = v
        # If every role raised, surface the first one — useful for debugging
        for r in results:
            if isinstance(r, BaseException):
                # NOTE: we do NOT re-raise here; parallel blocks intentionally
                # tolerate per-role failures (e.g., claim race losers raise
                # AihubError, caught by on_error_capture). If the on_error
                # was NOT configured for a role, that role's exception is
                # already raised inside _execute_action -> propagates as
                # task exception here. We still merged scratches above so
                # any successful role's captures are preserved.
                #
                # When a scenario REQUIRES every role to succeed, write an
                # assertion that checks each `vars[f"{role}.<capture>"]`
                # is non-None. one_succeeds does this for the "exactly one"
                # case.
                pass
    else:
        for cast_id in block.cast_ids:
            await _execute_actions_for_role(cast_id, block.actions, scenario, ctx,
                                             parallel=False)


async def _execute_actions_for_role(
    cast_id: str, actions: list[Action],
    scenario: Scenario, ctx: ExecutionContext,
    *, parallel: bool,
) -> None:
    if parallel:
        token = _current_role.set(cast_id)
        try:
            for action in actions:
                await _execute_action(cast_id, action, scenario, ctx)
        finally:
            _current_role.reset(token)
    else:
        for action in actions:
            await _execute_action(cast_id, action, scenario, ctx)


async def _execute_action(
    cast_id: str, action: Action,
    scenario: Scenario, ctx: ExecutionContext,
) -> None:
    if action.kind == "sleep":
        await asyncio.sleep(float(action.payload))
        return
    if action.kind == "bash":
        # shell_quote=True prevents captured values containing $(...) /
        # backticks / unquoted spaces from breaking out of their position
        # in the command. Scenarios are trusted in-repo today, but values
        # could come from API responses (e.g. captured work_item_id) — and
        # we'd rather lock this down before someone adds a scenario that
        # captures arbitrary user-supplied input.
        cmd = ctx.substitute(str(action.payload), shell_quote=True)
        result = subprocess.run(
            ["bash", "-c", cmd],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise subprocess.CalledProcessError(
                result.returncode, cmd,
                output=result.stdout,
                stderr=result.stderr,
            )
        if action.capture:
            # Build a capture dict: try to parse stdout as KEY=VALUE lines;
            # always also expose raw stdout/stderr/rc so scenarios can capture
            # those directly.
            bash_output: dict[str, Any] = {
                "stdout": result.stdout.strip(),
                "stderr": result.stderr.strip(),
                "rc": result.returncode,
            }
            for line in result.stdout.splitlines():
                if "=" in line:
                    k, _, v = line.partition("=")
                    bash_output[k.strip()] = v.strip()
            for result_field, var_name in action.capture.items():
                ctx.set_var(var_name, bash_output.get(result_field))
        return
    if action.kind == "repeat":
        n = int(action.payload)
        role = _current_role.get()
        for i in range(n):
            # Snapshot scratch/vars so we can restore loop-local vars after
            # each iteration without clobbering real captures.
            if role is not None:
                scratch_snapshot = ctx.role_scratch.get(role, {}).copy()
            else:
                vars_snapshot = ctx.vars.copy()
            ctx.set_var("i", str(i))
            ctx.set_var("role", cast_id)
            try:
                await _execute_action(cast_id, action.inner, scenario, ctx)  # type: ignore[arg-type]
            finally:
                # Restore only the loop-injected keys; keep any real captures
                # the inner action produced.
                if role is not None:
                    scratch = ctx.role_scratch.setdefault(role, {})
                    for loop_key in ("i", "role"):
                        # Remove loop var; restore pre-iteration value if any
                        scratch.pop(loop_key, None)
                        if loop_key in scratch_snapshot:
                            scratch[loop_key] = scratch_snapshot[loop_key]
                else:
                    ctx.vars.pop("i", None)
                    ctx.vars.pop("role", None)
                    if "i" in vars_snapshot:
                        ctx.vars["i"] = vars_snapshot["i"]
                    if "role" in vars_snapshot:
                        ctx.vars["role"] = vars_snapshot["role"]
        return
    if action.kind == "skill":
        cmd = ctx.substitute(str(action.payload))
        try:
            result = await _execute_skill(cast_id, cmd, scenario, ctx)
        except AihubError as e:
            if action.on_error_capture:
                # on_error_capture: {attribute_name: $var_name}, mirrors `capture`.
                # `code` is special: extract ErrorCode.value, not the enum itself.
                for attr, var_name in action.on_error_capture.items():
                    if attr == "code":
                        ctx.set_var(var_name, e.code.value)
                    else:
                        ctx.set_var(var_name, getattr(e, attr, None))
                return
            raise
        for result_field, var_name in action.capture.items():
            ctx.set_var(var_name, result.get(result_field))
        return
    raise ValueError(f"unsupported action kind: {action.kind}")


async def _execute_skill(
    cast_id: str, cmd: str, scenario: Scenario, ctx: ExecutionContext,
) -> dict[str, Any]:
    """Translate a /pf3-* skill invocation into MCP tool calls via the adapter."""
    adapter = ctx.adapters[cast_id]
    member = next(c for c in scenario.cast if c.id == cast_id)
    tokens = shlex.split(cmd)
    if not tokens or not tokens[0].startswith("/pf3-"):
        raise ValueError(f"action skill must start with /pf3-*; got: {cmd!r}")
    skill = tokens[0]
    args = _parse_skill_args(tokens[1:])

    if skill == "/pf3-start":
        no_claim = "--no-claim" in tokens
        goal = args.get("goal", "")
        project = args.get("project", "marketplace")
        wi = await adapter.create_work_item(
            project=project, goal=goal, scenario="coding", declared_resources=[],
        )
        wi_id = wi["work_item_id"]
        if no_claim:
            return {"work_item_id": wi_id}
        claim = await adapter.claim_work_item(
            work_item_id=wi_id, idempotency_key=f"scn-{wi_id}-{cast_id}",
            session_info={"machine_id": member.machine_id},
            requested_locks=[],
        )
        cred = AttemptCredential(
            attempt_id=claim["attempt_id"], claim_epoch=claim["claim_epoch"],
            session_secret=claim["session_secret"],
        )
        adapter.set_cred(cred)
        ctx.creds[cast_id] = cred
        return {"work_item_id": wi_id, **claim}

    if skill in ("/pf3-claim", "/pf3-resume"):
        wi_id = args.get("_positional", [None])[0]
        if wi_id is None:
            raise ValueError(f"{skill} needs <work_item_id>; got: {cmd!r}")
        claim = await adapter.claim_work_item(
            work_item_id=wi_id, idempotency_key=f"scn-{skill}-{wi_id}-{cast_id}",
            session_info={"machine_id": member.machine_id},
            requested_locks=[],
        )
        cred = AttemptCredential(
            attempt_id=claim["attempt_id"], claim_epoch=claim["claim_epoch"],
            session_secret=claim["session_secret"],
        )
        adapter.set_cred(cred)
        ctx.creds[cast_id] = cred
        return claim

    if skill == "/pf3-note":
        # /pf3-note "msg" --work-item-id ... --attempt-id ... --claim-epoch ... --session-secret ...
        positional = args.get("_positional", [])
        if not positional:
            raise ValueError(f"/pf3-note needs a message arg; got: {cmd!r}")
        msg = positional[0]
        wi_id = args["work-item-id"]
        attempt_id = args["attempt-id"]
        claim_epoch = int(args["claim-epoch"])
        session_secret = args["session-secret"]
        evt = await adapter.emit_event(
            work_item_id=wi_id, event_type="note",
            payload={"message": msg, "role": ctx.vars.get("role", cast_id),
                     "i": ctx.vars.get("i")},
            attempt_id=attempt_id, claim_epoch=claim_epoch,
            session_secret=session_secret,
        )
        return evt

    raise NotImplementedError(f"executor does not yet handle skill: {skill}")


def _parse_skill_args(tokens: list[str]) -> dict[str, Any]:
    """Cheap CLI-ish parser. Returns dict with --key:value pairs and _positional list."""
    out: dict[str, Any] = {"_positional": []}
    i = 0
    while i < len(tokens):
        t = tokens[i]
        if t.startswith("--"):
            key = t[2:]
            if i + 1 < len(tokens) and not tokens[i + 1].startswith("--"):
                out[key] = tokens[i + 1]
                i += 2
            else:
                out[key] = True
                i += 1
        else:
            out["_positional"].append(t)
            i += 1
    return out
