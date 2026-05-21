"""Unit tests for assertions._safe_eval_bool.

Pure functions, no fixtures, no DB. Fast. Catches regression in the
expression parser without standing up a PG container.
"""
from __future__ import annotations

import pytest

# assertions.py transitively imports polyforge_v3.* via executor.py. To keep
# this unit-test file portable to CI envs without the polyforge plugin, gate
# the import. (Future cleanup: split _safe_eval_bool into a standalone module
# with no polyforge deps so safe-eval can be unit-tested anywhere.)
pytest.importorskip(
    "polyforge_v3",
    reason="safe-eval tests transitively import polyforge_v3 via executor",
)

from tests.scenario_runner.assertions import _safe_eval_bool, _coerce, _apply


# ---------------------------------------------------------------------------
# Happy path: each operator
# ---------------------------------------------------------------------------

@pytest.mark.parametrize("expr,expected", [
    # int comparisons
    ("1 == 1",  True),
    ("1 == 2",  False),
    ("1 != 2",  True),
    ("3 < 5",   True),
    ("5 <= 5",  True),
    ("7 > 3",   True),
    ("7 >= 7",  True),
    # mixed: int vs string coerce
    ("2 == 2",  True),
    # float
    ("1.5 < 2.0",  True),
    ("1.0 == 1",   True),       # int/float crossover
    # string (quoted)
    ('"abc" == "abc"', True),
    ('"abc" != "xyz"', True),
    ("'foo' == 'foo'", True),
    # string (unquoted, e.g. substituted ULID)
    ("ra_01abc == ra_01abc",  True),
    ("ra_01abc != ra_02xyz",  True),
    # hyphenated (the I9 bug)
    ("zhang-mbp == zhang-mbp",  True),
    ("zhang-mbp != wang-mbp",   True),
    # URL/path-ish substrings (because of {workspace}/...)
    ("/tmp/x == /tmp/x",  True),
    ("a:b != a:c",        True),
])
def test_safe_eval_happy(expr: str, expected: bool):
    assert _safe_eval_bool(expr) is expected


# ---------------------------------------------------------------------------
# Rejection: malformed / dangerous input
# ---------------------------------------------------------------------------

@pytest.mark.parametrize("expr", [
    "import os",                  # python keyword
    "1 + 1",                      # arithmetic op not allowed
    "__import__('os')",           # call syntax
    "x;y == z",                   # semicolons disallowed
    "[1,2]",                      # brackets disallowed
    "x|y",                        # pipe disallowed
])
def test_safe_eval_rejects_dangerous(expr: str):
    with pytest.raises(ValueError):
        _safe_eval_bool(expr)


def test_safe_eval_missing_operator():
    with pytest.raises(ValueError, match="missing operator"):
        _safe_eval_bool("just_a_word")


# ---------------------------------------------------------------------------
# _coerce + _apply unit coverage
# ---------------------------------------------------------------------------

@pytest.mark.parametrize("raw,expected", [
    ("42",       42),
    ("-7",      -7),
    ("3.14",   3.14),
    ('"hi"',   "hi"),
    ("'hi'",   "hi"),
    ("bare",  "bare"),
    ("ra_01abc", "ra_01abc"),
    ("zhang-mbp", "zhang-mbp"),
])
def test_coerce(raw: str, expected: object):
    assert _coerce(raw) == expected


@pytest.mark.parametrize("op,lhs,rhs,expected", [
    ("==", 1, 1, True),
    ("==", 1, 2, False),
    ("!=", "a", "b", True),
    ("<",  1, 2, True),
    ("<=", 2, 2, True),
    (">",  3, 1, True),
    (">=", 3, 3, True),
])
def test_apply(op: str, lhs: object, rhs: object, expected: bool):
    assert _apply(op, lhs, rhs) is expected


def test_apply_unknown_op():
    with pytest.raises(ValueError):
        _apply("===", 1, 1)
