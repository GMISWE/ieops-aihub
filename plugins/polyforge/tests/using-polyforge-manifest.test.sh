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
#                 a new one fails, a resolved one fails until it is removed, and a
#                 baselined fragment that GROWS past its recorded character cap fails —
#                 without that cap the exemption is per-path, so relocating new ungated
#                 rule text into an already-exempt file costs nothing (aihub#296).
#   4. Compat:    the pre-aihub#295 parser, embedded verbatim below, must derive the
#                 SAME (path, condition) list from this manifest as the current one.
#                 Plugin and binary update through independent channels, so a manifest
#                 can meet a parser older than itself; the new attribute syntax must be
#                 invisible to that parser rather than merely survivable.
#   5. Metric:    prints the resident-tier lower bound = the total size of every
#                 fragment that is a rule with no gate. Nobody could compute this
#                 before, because the classification was never written down.
#   6. Notes:     references/manifest-notes.md (aihub#302: the maintainer block that
#                 used to live in SKILL.md's HTML comment) exists, is not gutted, still
#                 carries its load-bearing sections, and is pointed at from SKILL.md.
#   7. Controls:  nine negative fixtures that MUST fail, two that must pass, and a
#                 MUTATION run — the tier rule is deleted from a copy of this script
#                 and the tier fixture must then go green. A validator that passes on
#                 everything is indistinguishable from no validator.
#
# CI STATUS: WIRED (aihub#293). .github/workflows/ci.yml runs this as the step
# "aihub#293 using-polyforge manifest gate". A non-zero exit fails that step directly; on top
# of that it asserts named PASS markers, that no SKIP fired, that ALL PASS appears at least
# twice (lint AND controls), and a floor on the PASS count, to cover the case the exit code
# CANNOT report — a run that exits 0 having executed nothing.
# ADDING a check needs no CI change: the floor is `-ge`, a one-way ratchet. Only DELETING or
# RENAMING one of the markers that step greps for does.
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
# BASELINE: pre-existing violations. Each entry is (CHARACTER CAP, why it is exempt and who
# owns the fix). THIS LIST MAY ONLY SHRINK, in both of its dimensions. Adding to it is a
# deliberate, reviewable edit to a test file — which is the point: the previous cost of
# moving an unenforced rule out of the resident tier was zero, and that is what produced the
# aihub#285 regression.
#
# The CAP is not decoration (aihub#296). Keyed by path alone, this baseline priced one
# specific move at exactly zero: take new ungated rule text and put it inside a file that is
# already exempt. aihub#294 did precisely that — memory-conventions.md went from 2,155 to
# 4,841 characters and the lower bound printed in section 5 went from 11,489 to 14,175, with
# no assertion firing anywhere. An exemption that does not bound the thing it exempts is not
# an exception, it is an open channel.
#
# Direction, and why it is one-way: GROWTH FAILS, shrinking does not. Requiring the cap to be
# lowered on every shrink would make editing this number a routine part of editing these
# files — and a number that is routinely edited can no longer signal anything on the one edit
# that matters, the growth. (Same defect as a `gate:` that validates shape but not substance:
# cheapest to satisfy by weakening the claim.) The residue is that shrink-then-regrow is free
# up to the recorded cap, which is bounded by the size on the day the cap was set.
BASELINE = {
    "fragments/post-claim-routing.md": (4771,
        "moved on-demand by aihub#285 to stop the payload being truncated. It cannot "
        "come back until the resident tier is slimmed (aihub#296): making it resident "
        "measures 14,549 chars, against a 10,000-char harness limit."),
    "fragments/memory-conventions.md": (6457,
        "moved on-demand by aihub#285 on the grounds that memory-first.md states the "
        "memory-lives-in-aihub half more strictly. True for that half — but the "
        "link-discipline half (never put a `mem_...` id in a repo doc, nor a repo path "
        "in a memory) has NO resident copy, as SKILL.md's own on-demand rationale "
        "admits, and on-demand-index.md calls it 'the hard rule'. Also aihub#296. "
        "RAISED 4841 -> 6457 by aihub#313, naming the rule text as this cap requires: "
        "ONE new rule, '`fields=\"brief\"` — the axis to choose it on', whose normative "
        "sentence is 'brief a recall whose caller never reads a body — not the recalls "
        "that look big'. It is deliberately NOT resident: the resident payload measures "
        "8,452 of a 8,497 gate, and the rule is unenforceable in the abstract anyway "
        "because it is decided per call site. Its enforcement is therefore AT the call "
        "sites, not here — each briefed pf_recall in plugins/ states its reason inline "
        "and each deliberately-full one carries a `⚠️ No fields=\"brief\"` note naming "
        "the field it consumes, so the section here is rationale plus two numeric "
        "cautions (4-decimal rounding; the 0 follow-up rate was measured under FULL "
        "mode) rather than a rule that has to be obeyed unseen. The per-site table was "
        "cut from this section on purpose — it would have been a second copy of the "
        "inline notes, and a duplicated table rots."),
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
# The second dimension of the ratchet: an exempt fragment may not ABSORB new content.
sizes = {}
for p in sorted(set(BASELINE) & violations):
    cap = BASELINE[p][0]
    try:
        sizes[p] = len(open(os.path.join(skill_dir, p), encoding="utf-8").read())
    except OSError:
        sizes[p] = 0
    if sizes[p] > cap:
        bad("%s is baselined out of the resident tier at %d chars but now measures %d "
            "(+%d). The exemption is per-PATH, so moving new ungated rule text into an "
            "already-exempt file is the one way to enlarge the unenforced-rule surface "
            "with nothing firing — that is how aihub#294 took section 5's lower bound "
            "from 11,489 to 14,175. Shrink it back, or raise the cap here deliberately "
            "and name the rule text you are adding." % (p, cap, sizes[p], sizes[p] - cap))
if not violations.symmetric_difference(BASELINE):
    if violations:
        ok("no new violation; %d baselined: %s" % (len(violations), ", ".join(sorted(violations))))
        for p in sorted(violations):
            print("        ~ %s (%d/%d chars): %s" % (p, sizes.get(p, 0), BASELINE[p][0],
                                                      BASELINE[p][1]))
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

# --- 6. the maintainer notes -----------------------------------------------
# aihub#302 moved ~25,000 characters of maintainer reasoning out of an HTML comment at the
# top of SKILL.md into references/manifest-notes.md. That trade is only sound while the
# notes are still REACHABLE: the old arrangement could not lose them, because the rules sat
# in the file you had to open to edit the manifest. Moving them out created a single point
# of failure that no existing check covered — section 0 only validates declared @include /
# @ondemand paths, and this is neither. So assert the three things that make the move
# survivable: the file exists, it still carries its load-bearing sections, and SKILL.md
# points at it. Deleting any one of them must be red, not quietly green.
print()
print("6. maintainer notes — the block moved out of SKILL.md is still reachable")
NOTES_REL = "references/manifest-notes.md"
# A floor, deliberately not a ratchet: it exists to catch the notes being emptied or
# gutted, not to make every edit to them a test edit. Currently ~25,000 chars.
NOTES_MIN_CHARS = 15000
NOTES_ANCHORS = [
    "SIZE BUDGET",             # the two-sided payload ratchet and why not to raise it
    "MANIFEST SCHEMA",         # kind / gate / authority
    "THE TIER RULE",           # unenforced rule may not leave the resident tier
    "BACKWARD COMPATIBILITY",  # why @ondemand is a verb and not an attribute
]
notes_path = os.path.join(skill_dir, NOTES_REL)
notes = None
try:
    notes = open(notes_path, encoding="utf-8").read()
except OSError:
    bad("%s is missing. SKILL.md's pointer comment sends every future maintainer here for "
        "the SIZE BUDGET rule, the kind/gate/authority schema and the tier-rule rationale; "
        "with the file gone that pointer is a dead end and the rules are nowhere "
        "(aihub#302 moved them out of SKILL.md)." % NOTES_REL)
if notes is not None:
    if len(notes) < NOTES_MIN_CHARS:
        bad("%s is %d chars, below the %d-char floor. It held ~25,000 chars of maintainer "
            "reasoning when it was split out of SKILL.md; this floor exists so it cannot be "
            "emptied or stubbed out without saying so." % (NOTES_REL, len(notes), NOTES_MIN_CHARS))
    else:
        ok("%s present, %d chars (floor %d)" % (NOTES_REL, len(notes), NOTES_MIN_CHARS))
    absent = [a for a in NOTES_ANCHORS if a not in notes]
    if absent:
        bad("%s no longer contains: %s. These name the sections the manifest's own pointer "
            "promises are there." % (NOTES_REL, ", ".join(absent)))
    else:
        ok("%s still carries all %d load-bearing sections" % (NOTES_REL, len(NOTES_ANCHORS)))
    if NOTES_REL in manifest:
        ok("SKILL.md points at %s" % NOTES_REL)
    else:
        bad("SKILL.md does not mention %s anywhere. The notes then ship as a file nothing "
            "routes to, which is the 'moved somewhere nothing points at' failure this suite "
            "already guards against for on-demand fragments." % NOTES_REL)

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
echo "7. negative controls — each of these MUST turn the lint red"
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

# These three edit one attribute of one real entry. They locate the directive line and then
# the attribute BY NAME rather than matching a literal block: anchoring on a whole block made
# them silently stop mutating anything the moment aihub#296 changed three `kind:` values, and
# a negative control that mutates nothing passes for free.
r="$(fixture no_kind '
import sys, os
p = os.path.join(sys.argv[1], "SKILL.md")
rows = open(p, encoding="utf-8").read().split("\n")
i = rows.index("@include: fragments/output-format.md")
del rows[next(k for k in range(i + 1, len(rows)) if rows[k].startswith("kind:"))]
open(p, "w", encoding="utf-8").write("\n".join(rows))
')"
expect fail "an @include with no kind:" "$r"

r="$(fixture when_not_adjacent '
import sys, os
p = os.path.join(sys.argv[1], "SKILL.md")
rows = open(p, encoding="utf-8").read().split("\n")
i = rows.index("@include: fragments/repo-routing.md")
rows.insert(i + 2, "when: superpowers")   # second attribute line, i.e. NOT adjacent
open(p, "w", encoding="utf-8").write("\n".join(rows))
')"
expect fail "a when: that is not adjacent to its directive (old parser would drop it)" "$r"

r="$(fixture unjustified_authority '
import sys, os
p = os.path.join(sys.argv[1], "SKILL.md")
rows = open(p, encoding="utf-8").read().split("\n")
i = rows.index("@include: fragments/memory-first.md")
rows[next(k for k in range(i + 1, len(rows)) if rows[k].startswith("authority:"))] = \
    "authority: elsewhere"
open(p, "w", encoding="utf-8").write("\n".join(rows))
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

# The aihub#294 shape: not a new violation, not a new manifest entry — just new ungated rule
# text poured into a file the BASELINE already exempts. Before aihub#296 this cost zero.
GREW='
import sys, os
p = os.path.join(sys.argv[1], "fragments", "memory-conventions.md")
s = open(p, encoding="utf-8").read()
open(p, "w", encoding="utf-8").write(s.rstrip("\n") + "DELTA\n")
'
r="$(fixture baselined_fragment_grew "${GREW/DELTA/$(printf 'y%.0s' $(seq 200))}")"
expect fail "a baselined fragment absorbing 200 chars of new content" "$r"

# ...and the ratchet is deliberately ONE-WAY: shrinking such a fragment must stay green, or
# the cap becomes a number that gets edited on every touch and stops signalling anything.
r="$(fixture baselined_fragment_shrank '
import sys, os
p = os.path.join(sys.argv[1], "fragments", "memory-conventions.md")
s = open(p, encoding="utf-8").read()
open(p, "w", encoding="utf-8").write(s.rstrip("\n")[:-200] + "\n")
')"
expect pass "...the same fragment SHRINKING is accepted (the ratchet is one-way)" "$r"

# aihub#302: the maintainer notes are a new single point of failure — the rules used to
# live in the file you had to open to edit the manifest, and now they do not. Each of the
# three ways to lose them must be red.
r="$(fixture notes_deleted '
import sys, os
os.remove(os.path.join(sys.argv[1], "references", "manifest-notes.md"))
')"
expect fail "references/manifest-notes.md deleted outright" "$r"

r="$(fixture notes_gutted '
import sys, os
p = os.path.join(sys.argv[1], "references", "manifest-notes.md")
open(p, "w", encoding="utf-8").write("# maintainer notes\n\nTODO\n")
')"
expect fail "...or replaced by a stub that keeps the filename" "$r"

r="$(fixture notes_section_dropped '
import sys, os
p = os.path.join(sys.argv[1], "references", "manifest-notes.md")
s = open(p, encoding="utf-8").read()
# keep it well over the size floor, remove only the SIZE BUDGET heading text
open(p, "w", encoding="utf-8").write(s.replace("SIZE BUDGET", "size budget"))
')"
expect fail "...or still large but missing a load-bearing section" "$r"

r="$(fixture notes_pointer_removed '
import sys, os
p = os.path.join(sys.argv[1], "SKILL.md")
s = open(p, encoding="utf-8").read()
open(p, "w", encoding="utf-8").write(s.replace("references/manifest-notes.md", "somewhere"))
')"
expect fail "...or the file survives but SKILL.md stops pointing at it" "$r"

echo
echo "8. mutation control — delete the tier rule, the tier fixture must go green"
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
