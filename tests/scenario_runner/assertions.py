"""Evaluate the ## Assertions section of a parsed scenario.

Each assertion kind maps to a check function. All run sequentially; the
first failure raises AssertionError with diagnostic context.
"""
from __future__ import annotations

from pathlib import Path
from typing import Any

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncEngine

from .executor import ExecutionContext
from .parser import Assertion, Scenario


async def evaluate(
    scenario: Scenario,
    ctx: ExecutionContext,
    engine: AsyncEngine,
) -> None:
    for idx, a in enumerate(scenario.assertions):
        await _eval_one(a, idx, ctx, engine)


async def _eval_one(
    a: Assertion, idx: int, ctx: ExecutionContext, engine: AsyncEngine,
) -> None:
    if a.kind == "compare":
        expr = ctx.substitute(str(a.spec["value"]))
        # Tiny safe evaluator — only `==`, `!=`, `<`, `>` etc on substituted scalars
        if not _safe_eval_bool(expr):
            raise AssertionError(f"assertion #{idx} compare failed: {expr!r}")
        return

    if a.kind == "sql":
        query, binds = _bindify_sql(a.spec["query"], ctx)
        async with engine.connect() as conn:
            result = await conn.execute(sa.text(query), binds)
            if "expect_scalar" in a.spec:
                got = result.scalar_one()
                want = a.spec["expect_scalar"]
                if got != want:
                    raise AssertionError(
                        f"assertion #{idx} sql expect_scalar: query={query!r} "
                        f"got={got!r} want={want!r}"
                    )
            elif "expect_first_row" in a.spec:
                row = result.mappings().first()
                if row is None:
                    raise AssertionError(
                        f"assertion #{idx} sql expect_first_row: no row returned for {query!r}"
                    )
                for k, want in a.spec["expect_first_row"].items():
                    want_sub = ctx.substitute(str(want)) if isinstance(want, str) else want
                    got = row[k]
                    if str(got) != str(want_sub):
                        raise AssertionError(
                            f"assertion #{idx} sql expect_first_row: "
                            f"column {k}: got={got!r} want={want_sub!r}"
                        )
            else:
                raise ValueError(
                    f"sql assertion needs expect_scalar or expect_first_row; got {a.spec}"
                )
        return

    if a.kind == "file":
        path = Path(ctx.substitute(a.spec["path"]))
        if not path.exists():
            raise AssertionError(f"assertion #{idx} file: {path} does not exist")
        if "contains" in a.spec:
            content = path.read_text()
            needle = ctx.substitute(a.spec["contains"])
            if needle not in content:
                raise AssertionError(
                    f"assertion #{idx} file {path}: does not contain {needle!r}; "
                    f"actual content begins: {content[:200]!r}"
                )
        return

    if a.kind == "one_succeeds":
        # Parallel-cast invariant: exactly one named actor populated the
        # `success_capture` var (i.e., their action returned successfully).
        # Captures from parallel blocks are stored under `f"{role}.{var}"`
        # by the executor (executor._execute_block merge step), so we look
        # at those scoped keys explicitly.
        success_capture = a.spec["success_capture"]
        actors = a.spec["actors"]
        n_success = sum(
            1 for actor in actors
            if ctx.vars.get(f"{actor}.{success_capture}") is not None
        )
        if n_success != 1:
            details = {
                actor: ctx.vars.get(f"{actor}.{success_capture}")
                for actor in actors
            }
            raise AssertionError(
                f"assertion #{idx} one_succeeds: expected exactly 1 success in "
                f"{actors}, found {n_success}. Per-actor `{success_capture}`: {details}"
            )
        return

    if a.kind == "loser":
        # `loser:` requires `var: $X` referencing the user-named var
        # populated by `on_error_capture`. Two lookup paths:
        #   - Sequential capture: `vars[X]`
        #   - Parallel capture: `vars[f"{role}.X"]` for the role that lost
        # Returns the first non-None match (typically only one — the loser).
        var_ref = a.spec.get("var")
        if not var_ref or not isinstance(var_ref, str):
            raise ValueError(
                f"assertion #{idx} loser: must specify `var: $X` naming the error var"
            )
        key = var_ref.lstrip("$")
        candidates: list[tuple[str, Any]] = []
        # Sequential
        if key in ctx.vars and ctx.vars[key] is not None:
            candidates.append((key, ctx.vars[key]))
        # Parallel (per-role prefixed)
        for k, v in ctx.vars.items():
            if k.endswith(f".{key}") and v is not None:
                candidates.append((k, v))
        if not candidates:
            raise AssertionError(
                f"assertion #{idx} loser: var {var_ref} not captured (no role's "
                f"`on_error` fired, or scenario misconfigured). vars keys: "
                f"{sorted(ctx.vars.keys())}"
            )
        if len(candidates) > 1:
            raise AssertionError(
                f"assertion #{idx} loser: var {var_ref} captured by multiple "
                f"sources {[c[0] for c in candidates]} — at most one should lose."
            )
        want = a.spec["err_code"]
        got = candidates[0][1]
        if got != want:
            raise AssertionError(
                f"assertion #{idx} loser ({candidates[0][0]}): "
                f"got {got!r} want {want!r}"
            )
        return

    if a.kind == "no_payload_collision":
        query, binds = _bindify_sql(a.spec["query"], ctx)
        async with engine.connect() as conn:
            rows = (await conn.execute(sa.text(query), binds)).fetchall()
        distinct = len({tuple(r) for r in rows})
        want = a.spec["expect_distinct_count"]
        if distinct != want:
            raise AssertionError(
                f"assertion #{idx} no_payload_collision: "
                f"got {distinct} distinct, want {want}; query={query!r}"
            )
        return

    raise ValueError(f"unknown assertion kind: {a.kind}")


def _bindify_sql(raw: str, ctx: ExecutionContext) -> tuple[str, dict[str, Any]]:
    """Convert $VAR placeholders to :VAR bind params; resolve via ctx.vars.

    Distinct from ctx.substitute (which does string replacement) — for SQL we
    need parameterized binds so PG sees the values as literals, not identifiers.
    Plain {workspace} string substitution still uses substitute.
    """
    import re
    out = raw.replace("{workspace}", str(ctx.workspace))
    binds: dict[str, Any] = {}

    def _repl(m):
        name = m.group(1)
        if name not in ctx.vars:
            raise KeyError(f"SQL references unknown var $${name}")
        binds[name] = ctx.vars[name]
        return f":{name}"

    out = re.sub(r"\$(\w+)", _repl, out)
    return out, binds


def _safe_eval_bool(expr: str) -> bool:
    """Evaluate a tiny boolean expression with literals + ==/!=/</> only.

    Permitted character set: word chars (incl. hyphens — common in ULIDs +
    machine_ids), whitespace, dots, comparison operators, quotes, slashes
    and colons (so URL-ish substrings survive substitution).

    Coercion: numeric literals → int/float; quoted strings → str sans quotes;
    bare tokens → str. So `compare: 1 == 1` works, `compare: "ra-..." != "rb-..."` works.

    The unit test `test_safe_eval_unit.py` exhaustively exercises each
    operator + coercion path; expand there before relaxing this regex.
    """
    import re
    if not re.fullmatch(r"[\w\s\.\=\!\<\>\"\'\-/:]+", expr):
        raise ValueError(f"unsafe compare expr: {expr!r}")
    # Order matters: longest operators first (e.g., '==' must match before '=').
    for op in ("==", "!=", "<=", ">=", "<", ">"):
        if op in expr:
            lhs, rhs = expr.split(op, 1)
            lhs_v = _coerce(lhs.strip())
            rhs_v = _coerce(rhs.strip())
            return _apply(op, lhs_v, rhs_v)
    raise ValueError(f"compare expr missing operator: {expr!r}")


def _coerce(s: str) -> Any:
    if s.startswith('"') and s.endswith('"'):
        return s[1:-1]
    if s.startswith("'") and s.endswith("'"):
        return s[1:-1]
    try:
        return int(s)
    except ValueError:
        pass
    try:
        return float(s)
    except ValueError:
        pass
    return s  # bare string


def _apply(op: str, l: Any, r: Any) -> bool:
    if op == "==": return l == r
    if op == "!=": return l != r
    if op == "<":  return l <  r
    if op == "<=": return l <= r
    if op == ">":  return l >  r
    if op == ">=": return l >= r
    raise ValueError(op)
