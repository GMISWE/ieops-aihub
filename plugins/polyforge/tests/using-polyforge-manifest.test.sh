#!/usr/bin/env bash
# Manifest semantics gate for skills/using-polyforge/SKILL.md (aihub#295).
#
# WHY THIS EXISTS
# ---------------
# The manifest used to say only WHICH fragments and IN WHAT ORDER. It could not say
# whether a fragment is information or a rule, nor which mechanism enforces a rule.
# That gap has already cost something concrete: aihub#285 moved post-claim-routing.md
# (4,771 chars, the largest fragment) to the on-demand tier. It is a RULE, and nothing
# enforces it. For an unenforced rule that is not in context, "should have read it and
# didn't" and "read it and ignored it" are the SAME OBSERVATION from outside — both
# silent. The agent that moved it gave a source-based justification, but the criterion
# "an unenforced rule must not leave the resident tier" existed in no spec, so the next
# person moving a fragment had nothing to apply.
#
# Measured basis (aihub#295, 2026-08-30): 6 rules ship in this skill; exactly ONE has an
# enforcing mechanism (IR1, via hooks/pf-commit-guard's worktree + attribution gates).
# The other five — IR2, IR3, the three-segment output format, NL routing, and post-claim
# Next-steps routing — have none. memory-conventions.md's link discipline is a seventh,
# added on review: three places in this tree already call its content a rule. The kind:
# line is not settled and only ever moves one way, outward; aihub#296 owns settling it.
#
# WHAT IS ASSERTED
#   0. Coverage:  every file under fragments/ is declared exactly once, and every
#                 declared path exists. No orphans, no ghosts.
#   1. Schema:    every directive carries kind (info|rule); gate is present iff
#                 kind is rule; gate-partial only alongside `gate: none`; authority
#                 is always present; resident-because is present iff a RESIDENT entry
#                 has a non-`self` authority.
#   2. Gates:     every mechanism named in gate/gate-partial is on the CURATED list of
#                 things that genuinely make a violation observable — today exactly one,
#                 hooks/pf-commit-guard. Existing on disk is deliberately NOT the test
#                 (see ENFORCEMENT_MECHANISMS for why that was tried and was wrong), but
#                 each curated entry is checked against disk so the list cannot rot.
#   3. TIER RULE: kind: rule + gate: none  =>  must NOT leave the resident tier.
#                 Pre-existing violations live in a BASELINE that may only shrink:
#                 a new one fails, and a resolved one fails until it is removed.
#   4. Compat:    the pre-aihub#295 parser, embedded verbatim below, must derive the
#                 SAME (path, condition) list from this manifest as the current one.
#                 Plugin and binary update through independent channels, so a manifest
#                 can meet a parser older than itself; the new attribute syntax must be
#                 invisible to that parser rather than merely survivable.
#   5. Metric:    prints the resident-tier lower bound = the total size of every
#                 fragment that is a rule with no gate. Nobody could compute this
#                 before, because the classification was never written down.
#   6. Controls:  five negative fixtures that MUST fail, one that must pass, and a
#                 MUTATION run — the tier rule is deleted from a copy of this script
#                 and the tier fixture must then go green. A validator that passes on
#                 everything is indistinguishable from no validator.
#
# !! CI STATUS: THIS SUITE IS NOT WIRED INTO CI YET. !!
# .github/workflows/ci.yml runs only launcher-update-check.test.sh. Wiring this in is
# aihub#293 (ci.yml is owned by a concurrent work item). A gate that CI does not run is
# a gate nobody runs — until #293 lands this is a manual check, NOT a guarantee.
#
# USAGE
#   tests/using-polyforge-manifest.test.sh              # lint + all controls
#   tests/using-polyforge-manifest.test.sh <plugin_dir> # lint that tree only (fixtures)

set -uo pipefail

command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 unavailable"; exit 0; }

here="$(cd "$(dirname "$0")" && pwd)"
self="$here/$(basename "$0")"
default_root="$(cd "$here/.." && pwd)"

# ---------------------------------------------------------------------------
# The lint core. stdout = human-readable findings; exit 1 iff anything failed.
# ---------------------------------------------------------------------------
lint() { # <plugin_root>
PF_ROOT="$1" python3 <<'PY'
import os, re, sys

root = os.environ["PF_ROOT"]
skill_dir = os.path.join(root, "skills", "using-polyforge")
manifest_path = os.path.join(skill_dir, "SKILL.md")
if not root or not os.path.isdir(skill_dir):
    # Guard, not politeness: an empty PF_ROOT would resolve to a RELATIVE path and, run
    # from the plugin root, silently lint the real tree instead of the fixture — which
    # makes every negative control pass vacuously.
    print("FAIL: PF_ROOT=%r is not a plugin root (no skills/using-polyforge)" % root,
          file=sys.stderr)
    sys.exit(1)

fails = []
def ok(m):  print("  PASS: %s" % m)
def bad(m): print("  FAIL: %s" % m, file=sys.stderr); fails.append(m)

try:
    manifest = open(manifest_path, encoding="utf-8").read()
except OSError as e:
    print("FAIL: cannot read %s (%s)" % (manifest_path, e), file=sys.stderr)
    sys.exit(1)

# Strip YAML frontmatter exactly the way hooks/pf-session-start does.
def strip_frontmatter(text):
    if not text.startswith("---"):
        return text
    rows = text.split("\n")
    for i in range(1, len(rows)):
        if rows[i].strip() == "---":
            return "\n".join(rows[i + 1:])
    return text

body = strip_frontmatter(manifest)
lines = body.split("\n")

# --- current grammar ------------------------------------------------------
directive_re = re.compile(r'^@(include|ondemand):\s*(\S+)\s*$')
attr_re = re.compile(r'^([A-Za-z][A-Za-z0-9_-]*):\s*(.*)$')

entries = []  # {verb, path, attrs: [(k, v)], line}
i = 0
while i < len(lines):
    m = directive_re.match(lines[i].strip())
    if not m:
        i += 1
        continue
    e = {"verb": m.group(1), "path": m.group(2), "attrs": [], "line": i + 1}
    i += 1
    while i < len(lines):
        s = lines[i].strip()
        if not s or directive_re.match(s):
            break
        a = attr_re.match(s)
        if not a:
            break
        e["attrs"].append((a.group(1).lower(), a.group(2).strip()))
        i += 1
    entries.append(e)

if not entries:
    print("FAIL: manifest declares no fragments at all", file=sys.stderr)
    sys.exit(1)

def attrs_of(e):
    d = {}
    for k, v in e["attrs"]:
        d.setdefault(k, v)
    return d

resident = [e for e in entries if e["verb"] == "include"]
ondemand = [e for e in entries if e["verb"] == "ondemand"]

print("using-polyforge manifest: %d declared (%d resident, %d on-demand)"
      % (len(entries), len(resident), len(ondemand)))
print()

# --- 0. coverage ----------------------------------------------------------
print("0. coverage — every fragment declared exactly once, every declaration real")
frag_dir = os.path.join(skill_dir, "fragments")
on_disk = set()
if os.path.isdir(frag_dir):
    on_disk = {"fragments/" + f for f in os.listdir(frag_dir) if f.endswith(".md")}
declared = [e["path"] for e in entries]
dupes = sorted({p for p in declared if declared.count(p) > 1})
if dupes:
    bad("declared more than once: %s" % ", ".join(dupes))
else:
    ok("no fragment declared twice")

missing_file = [p for p in declared if not os.path.isfile(os.path.join(skill_dir, p))]
if missing_file:
    bad("declared but not on disk: %s (a directive inside the HTML comment will land "
        "here — the parser strips each line before matching)" % ", ".join(missing_file))
else:
    ok("every declared path exists on disk")

undeclared = sorted(on_disk - set(declared))
if undeclared:
    bad("on disk but declared nowhere: %s — an undeclared fragment is invisible to "
        "every check in this file" % ", ".join(undeclared))
else:
    ok("no undeclared fragment under fragments/ (%d files)" % len(on_disk))

# --- gate vocabulary: CURATED, then checked against disk -------------------
# This list was originally DISCOVERED (everything in hooks/ plus every tests/*.test.*).
# That was wrong in a way worth recording, because it is the same defect as the
# `authority: self` hole below: a field that validates SHAPE but not SUBSTANCE is
# cheapest to satisfy by weakening the claim, not by complying. Discovery admitted 15
# names of which exactly ONE enforces anything; `gate: pf-session-start` (the assembler),
# `gate: pf-skill-router` (an injector that by its own header "never blocks or denies")
# and even `gate: using-polyforge-manifest.test.sh` (this file — a rule gated by the lint
# that checks it is gated) all passed green. Compare the other escape hatch, BASELINE:
# that one is deliberately loud, a constant in a test file. A fake gate name was strictly
# quieter — one line in the manifest you are already editing — and came with a green tick.
#
# So: to be nameable as a gate, a mechanism must actually make a violation OBSERVABLE.
# Adding to this list is a deliberate edit to a test file, and the claim must be true.
ENFORCEMENT_MECHANISMS = {
    # hooks/pf-commit-guard — PreToolUse on Bash + pf_commit/pf_pr/pf_wrap/pf_ship.
    # It DENYs (three deny sites), so violating IR1 fails loudly at the tool call.
    "pf-commit-guard": "hooks/pf-commit-guard",
    # Nothing else in this plugin enforces agent BEHAVIOUR. hooks/pf-session-start and
    # hooks/pf-skill-router inject context; hooks/pf-repo-sync syncs repos; every
    # tests/*.test.* gates repo CONTENT at review time, which cannot observe an agent
    # ignoring a rule mid-session. A test may be listed here only if a rule's violation
    # really does show up as a red test.
}
known_gates = set(ENFORCEMENT_MECHANISMS) | {"none"}
# Existence check, so a curated entry cannot outlive the mechanism it names.
rotted = [n for n, rel in sorted(ENFORCEMENT_MECHANISMS.items())
          if not os.path.exists(os.path.join(root, rel))]

# --- 1./2. schema + gate vocabulary ---------------------------------------
print()
print("1. schema — kind / gate / authority / resident-because")
KINDS = ("info", "rule")
for e in entries:
    d = attrs_of(e)
    tag = "%s (line %d)" % (e["path"], e["line"])

    kind = d.get("kind")
    if kind is None:
        bad("%s: no `kind:` — every directive must declare info or rule" % tag)
        continue
    if kind not in KINDS:
        bad("%s: kind: %s is not one of %s" % (tag, kind, "|".join(KINDS)))
        continue

    gate = d.get("gate")
    if kind == "rule" and gate is None:
        bad("%s: kind: rule with no `gate:` — name the mechanism that catches a "
            "violation, or write `gate: none`" % tag)
    if kind == "info" and gate is not None:
        bad("%s: kind: info must not carry `gate:` (nothing to enforce)" % tag)
    if gate is not None and not gate.strip():
        bad("%s: empty `gate:` — write `none` explicitly" % tag)

    gp = d.get("gate-partial")
    if gp is not None and (kind != "rule" or (gate or "").strip() != "none"):
        bad("%s: `gate-partial:` is only meaningful on a rule whose gate is `none`" % tag)

    for field in ("gate", "gate-partial"):
        raw = d.get(field)
        if not raw:
            continue
        for mech in [x.strip() for x in raw.split(",") if x.strip()]:
            if mech not in known_gates:
                bad("%s: %s names `%s`, which is not an ENFORCEMENT mechanism. Existing "
                    "on disk is not enough — an assembler, an injector or a test that "
                    "reads repo content cannot observe an agent ignoring a rule, so "
                    "naming one would claim a gate that does not exist. Enforcing "
                    "today: %s. If `%s` really does make a violation observable, add it "
                    "to ENFORCEMENT_MECHANISMS in this file and say how."
                    % (tag, field, mech, ", ".join(sorted(ENFORCEMENT_MECHANISMS)), mech))

    authority = d.get("authority")
    if not authority:
        bad("%s: no `authority:` — write `self`, or point at the maintained copy" % tag)
    else:
        rb = d.get("resident-because")
        needs_rb = (e["verb"] == "include" and authority != "self")
        if needs_rb and not rb:
            bad("%s: resident, but the maintained copy is `%s`. Add "
                "`resident-because:` — why does a non-authoritative copy spend "
                "resident budget?" % (tag, authority))
        if rb and not needs_rb:
            bad("%s: `resident-because:` is only for a RESIDENT entry whose authority "
                "is not `self`" % tag)

    when = d.get("when")
    if when is not None:
        if e["verb"] != "include":
            bad("%s: `when:` on a non-resident directive has no effect" % tag)
        elif e["attrs"][0][0] != "when":
            bad("%s: `when:` must be the FIRST attribute line. The pre-aihub#295 parser "
                "only looks at the line immediately after the directive; anywhere else "
                "it silently loses the condition and injects unconditionally." % tag)
if not fails:
    ok("all %d entries carry a well-formed attribute block" % len(entries))
for n in rotted:
    bad("ENFORCEMENT_MECHANISMS names `%s`, but %s is not on disk — the curated list "
        "has rotted, and every `gate: %s` in the manifest is now a false claim."
        % (n, ENFORCEMENT_MECHANISMS[n], n))
if not rotted:
    ok("every curated enforcement mechanism still exists on disk (%s)"
       % ", ".join("%s -> %s" % (n, p) for n, p in sorted(ENFORCEMENT_MECHANISMS.items())))

# --- 3. the tier rule -----------------------------------------------------
# BASELINE: pre-existing violations, each with the work item that owns the fix.
# THIS LIST MAY ONLY SHRINK. Adding to it is a deliberate, reviewable edit to a test
# file — which is the point: the previous cost of moving an unenforced rule out of the
# resident tier was zero, and that is what produced the aihub#285 regression.
BASELINE = {
    "fragments/post-claim-routing.md":
        "moved on-demand by aihub#285 to stop the payload being truncated. It cannot "
        "come back until the resident tier is slimmed (aihub#296): making it resident "
        "measures 14,544 chars, against a 10,000-char harness limit.",
    "fragments/memory-conventions.md":
        "moved on-demand by aihub#285 on the grounds that memory-first.md states the "
        "memory-lives-in-aihub half more strictly. True for that half — but the "
        "link-discipline half (never put a `mem_...` id in a repo doc, nor a repo path "
        "in a memory) has NO resident copy, as SKILL.md's own on-demand rationale "
        "admits, and on-demand-index.md calls it 'the hard rule'. Also aihub#296.",
}

print()
print("3. TIER RULE — an unenforced rule must not leave the resident tier")
# >>> TIER RULE (aihub#295) — the mutation control deletes everything between these two
# >>> markers and re-runs the tier fixture; that run MUST then go green. Both directions
# >>> of the ratchet live inside the markers, so the mutant reports nothing at all
# >>> rather than failing for an unrelated reason.
violations = set()
for e in entries:
    d = attrs_of(e)
    if d.get("kind") == "rule" and (d.get("gate") or "").strip() == "none" \
       and e["verb"] != "include":
        violations.add(e["path"])
for p in sorted(violations - set(BASELINE)):
    bad("%s is `kind: rule` + `gate: none` but is NOT resident. Nothing enforces it, "
        "so out of context it has no observable failure mode: 'never read it' and "
        "'read it and ignored it' look identical. Keep it resident. Naming something "
        "in `gate:` is NOT the way out of this: only a mechanism that genuinely makes "
        "the violation observable counts (see ENFORCEMENT_MECHANISMS), and if one "
        "existed this fragment would not be here. If it truly cannot be resident, that "
        "is a budget problem — add it to BASELINE with the work item that owns the "
        "fix." % p)
for p in sorted(set(BASELINE) - violations):
    bad("%s is in the tier-rule BASELINE but no longer violates it. Delete its entry "
        "from BASELINE in this file — the baseline is a ratchet and must only shrink."
        % p)
if not violations.symmetric_difference(BASELINE):
    if violations:
        ok("no new violation; %d baselined: %s" % (len(violations), ", ".join(sorted(violations))))
        for p in sorted(violations):
            print("        ~ %s: %s" % (p, BASELINE[p]))
    else:
        ok("no rule with `gate: none` sits outside the resident tier")
# <<< TIER RULE <<<

# --- 4. backward compatibility -------------------------------------------
print()
print("4. compat — the pre-aihub#295 parser reads this manifest identically")
def legacy_parse(text):
    """hooks/pf-session-start's manifest loop as it stood before aihub#295, verbatim."""
    inc_re = re.compile(r'^@include:\s*(\S+)\s*$')
    when_re = re.compile(r'^when:\s*(\S+)\s*$')
    rows = strip_frontmatter(text).split("\n")
    out = []
    j = 0
    while j < len(rows):
        m = inc_re.match(rows[j].strip())
        if not m:
            j += 1
            continue
        path = m.group(1)
        cond = None
        if j + 1 < len(rows):
            w = when_re.match(rows[j + 1].strip())
            if w:
                cond = w.group(1)
                j += 1
        j += 1
        out.append((path, cond))
    return out

legacy = legacy_parse(manifest)
current = [(e["path"], attrs_of(e).get("when")) for e in resident]
if legacy == current:
    ok("old parser derives the same %d (path, condition) pairs — new keys are invisible "
       "to it, and no on-demand entry would be injected by it" % len(current))
else:
    only_old = [x for x in legacy if x not in current]
    only_new = [x for x in current if x not in legacy]
    bad("old parser would assemble a DIFFERENT skill. Old-only: %s. New-only: %s. An "
        "on-demand entry spelled `@include:` would be injected by an old hook and "
        "silently blow the size budget; a `when:` that is not adjacent to its directive "
        "would lose its condition there." % (only_old or "-", only_new or "-"))

# --- 5. the derived metric -----------------------------------------------
print()
print("5. metric — lower bound on the resident tier")
ungated = []
for e in entries:
    d = attrs_of(e)
    if d.get("kind") == "rule" and (d.get("gate") or "").strip() == "none":
        try:
            n = len(open(os.path.join(skill_dir, e["path"]), encoding="utf-8").read())
        except OSError:
            n = 0
        ungated.append((e["path"], n, e["verb"]))
total = sum(n for _, n, _ in ungated)
for p, n, v in sorted(ungated, key=lambda x: -x[1]):
    print("     %6d  %s%s" % (n, p, "" if v == "include" else "   [NOT RESIDENT]"))
print("     %6d  = SUM(kind:rule AND gate:none)  <- the resident tier cannot be "
      "smaller than this" % total)

print()
if fails:
    print("%d CHECK(S) FAILED" % len(fails), file=sys.stderr)
    sys.exit(1)
print("ALL PASS")
sys.exit(0)
PY
}

# ---------------------------------------------------------------------------
# Lint-only mode: used by the negative controls and by the mutation control.
# ---------------------------------------------------------------------------
if [ "$#" -ge 1 ]; then
  lint "$1"
  exit $?
fi

# ---------------------------------------------------------------------------
# Full run: lint the real tree, then prove the checks can fail.
# ---------------------------------------------------------------------------
fails=0
lint "$default_root" || fails=$((fails + 1))

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t pfmanifest)"
trap 'rm -rf "$tmp"' EXIT

# fixture <name> <python-mutation> -> echoes the fixture root
fixture() {
  local name dst prog
  name="$1"
  prog="$2"
  dst="$tmp/$name"
  rm -rf "$dst"
  cp -r "$default_root" "$dst" || return 1
  python3 -c "$prog" "$dst/skills/using-polyforge" >&2 || return 1
  echo "$dst"
}

expect() { # <expected: fail|pass> <label> <root> [script]
  local want label root script out rc
  want="$1"
  label="$2"
  root="$3"
  script="${4:-$self}"
  if [ -z "$root" ] || [ ! -d "$root" ]; then
    echo "  FAIL: $label — fixture was never built (root='$root')" >&2
    fails=$((fails + 1))
    return
  fi
  out="$(bash "$script" "$root" 2>&1)"; rc=$?
  if { [ "$want" = fail ] && [ "$rc" -ne 0 ]; } || { [ "$want" = pass ] && [ "$rc" -eq 0 ]; }; then
    echo "  PASS: $label (exit $rc)"
    if [ "$want" = fail ]; then
      printf '%s\n' "$out" | grep -m1 'FAIL:' | cut -c1-160 | sed 's/^ */        > /'
    fi
    return 0
  fi
  echo "  FAIL: $label — expected the lint to $want, it exited $rc" >&2
  printf '%s\n' "$out" | sed 's/^/        /' >&2
  fails=$((fails + 1))
}

# The probe fragment: a rule that nothing enforces, parked in the on-demand tier.
PROBE_WRITE='
import sys, os
d = sys.argv[1]
open(os.path.join(d, "fragments", "_probe.md"), "w").write("## probe rule\nAlways do the thing.\n")
p = os.path.join(d, "SKILL.md"); s = open(p, encoding="utf-8").read()
s = s.rstrip("\n") + "\n\n@ondemand: fragments/_probe.md\nkind: rule\ngate: GATE\nauthority: self\n"
open(p, "w", encoding="utf-8").write(s)
'

echo
echo "6. negative controls — each of these MUST turn the lint red"
r="$(fixture ungated_ondemand "${PROBE_WRITE/GATE/none}")"
expect fail "an ungated rule declared on-demand (the aihub#285 shape)" "$r"
tier_fixture="$r"

r="$(fixture gated_ondemand "${PROBE_WRITE/GATE/pf-commit-guard}")"
expect pass "...the same fragment with a real gate named is accepted" "$r"

r="$(fixture bogus_gate "${PROBE_WRITE/GATE/worktree-check}")"
expect fail "a gate name that exists nowhere at all" "$r"

# The three names below all EXIST on disk and all enforce nothing. When the vocabulary
# was discovered rather than curated, every one of them turned this lint green — which
# made `gate:` a shape check, satisfiable by weakening the claim instead of complying.
for fake in pf-session-start pf-skill-router using-polyforge-manifest.test.sh; do
  r="$(fixture "fake_gate_${fake//./_}" "${PROBE_WRITE/GATE/$fake}")"
  expect fail "gate: $fake — exists on disk, enforces nothing" "$r"
done

r="$(fixture no_kind '
import sys, os, re
p = os.path.join(sys.argv[1], "SKILL.md"); s = open(p, encoding="utf-8").read()
s = s.replace("@include: fragments/output-format.md\nkind: rule\n",
              "@include: fragments/output-format.md\n", 1)
open(p, "w", encoding="utf-8").write(s)
')"
expect fail "an @include with no kind:" "$r"

r="$(fixture when_not_adjacent '
import sys, os
p = os.path.join(sys.argv[1], "SKILL.md"); s = open(p, encoding="utf-8").read()
s = s.replace("@include: fragments/repo-routing.md\nkind: info\n",
              "@include: fragments/repo-routing.md\nkind: info\nwhen: superpowers\n", 1)
open(p, "w", encoding="utf-8").write(s)
')"
expect fail "a when: that is not adjacent to its directive (old parser would drop it)" "$r"

r="$(fixture unjustified_authority '
import sys, os
p = os.path.join(sys.argv[1], "SKILL.md"); s = open(p, encoding="utf-8").read()
s = s.replace("@include: fragments/memory-first.md\nkind: info\nauthority: self\n",
              "@include: fragments/memory-first.md\nkind: info\nauthority: elsewhere\n", 1)
open(p, "w", encoding="utf-8").write(s)
')"
expect fail "a resident fragment whose authority is elsewhere, with no justification" "$r"

r="$(fixture rotted_curation '
import sys, os
os.remove(os.path.join(os.path.dirname(sys.argv[1]), "..", "hooks", "pf-commit-guard"))
')"
expect fail "a curated enforcement mechanism that is no longer on disk" "$r"

r="$(fixture orphan_fragment '
import sys, os
open(os.path.join(sys.argv[1], "fragments", "_orphan.md"), "w").write("nobody declared me\n")
')"
expect fail "a fragment on disk that the manifest never declares" "$r"

echo
echo "7. mutation control — delete the tier rule, the tier fixture must go green"
mut="$tmp/mutant.sh"
python3 - "$self" "$mut" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
rows = open(src, encoding="utf-8").read().split("\n")
start = next(i for i, r in enumerate(rows) if ">>> TIER RULE (aihub#295)" in r)
end = next(i for i, r in enumerate(rows) if "<<< TIER RULE <<<" in r)
kept = rows[:start] + ["pass  # tier rule excised by the mutation control"] + rows[end + 1:]
open(dst, "w", encoding="utf-8").write("\n".join(kept))
PY
chmod +x "$mut"
expect pass "with the tier rule excised, the ungated-on-demand fixture no longer fails" \
       "$tier_fixture" "$mut"
expect fail "...and the un-mutated script still fails it (the red came from the rule)" \
       "$tier_fixture"

echo
if [ "$fails" -eq 0 ]; then
  echo "ALL PASS"
  exit 0
fi
echo "$fails CHECK(S) FAILED" >&2
exit 1
