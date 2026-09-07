#!/usr/bin/env python3
"""Mine the local Claude Code transcript corpus for the real polyforge MCP
request/response record (aihub#412).

The transcripts under $PF_CORPUS_DIR are the only end-to-end record of the
polyforge -> MCP -> aihub chain: the server keeps no per-request log and
agent_events is a whitelisted semantic stream. This script turns them into
six committed artifacts, from ONE streaming pass over the corpus:

  1. per-tool census: calls / errors / error rate / first+last seen
        -> <docs>/tool-census.md, <docs>/tool-census.csv
  2. normalized error taxonomy (ids, slugs, numbers, hashes, paths stripped)
        -> <docs>/error-taxonomy.md
  3. per-tool per-param observed JSON types and low-cardinality values,
     diffed against the published schema contract
        -> <docs>/param-types-vs-schema.md
  4. sanitized golden request/response fixtures, up to 3 per observed tool
        -> <fixtures>/<tool>/{happy,error,edge}.json
  5. sequence inventory: per (transcript, work item) flow classification
        -> <docs>/sequence-inventory.md
  6. hop5 ratchet prep: union of top-level response keys ever observed
        -> <docs>/response-keys/<tool>.json
  plus <docs>/run-manifest.json -- the denominators (files, lines, parse
  failures, timestamp-fallback counts) every number above is measured against.

Corpus root is never hard-coded: pass --corpus or set PF_CORPUS_DIR.
Standard library only.

SANITIZATION IS A GATE, not a step. Every string that reaches a fixture goes
through sanitize_text(); secret-bearing and free-text fields are replaced
structurally by field name before that. internal/mcp/corpus_fixture_sanitize_test.go
re-scans the committed tree and fails on anything that slips through, so this
script and that test must be changed together.
"""

import argparse
import csv
import datetime as _dt
import hashlib
import json
import os
import re
import sys
from collections import Counter, defaultdict

TOOL_PREFIX = "mcp__plugin_polyforge_polyforge__pf_"
NAME_NEEDLE = b'"' + TOOL_PREFIX.encode()
RESULT_NEEDLE = b'"tool_use_id"'
_TOOL_USE_ID_RE = re.compile(rb'"tool_use_id"\s*:\s*"([^"]+)"')
# The versioned skill path is the only per-transcript plugin-version signal:
# the JSONL envelope carries the Claude Code version, never the plugin's.
_PLUGIN_VER_RE = re.compile(rb"polyforge/(1\.1\.\d+)/skills")

# ---------------------------------------------------------------------------
# sanitization
# ---------------------------------------------------------------------------

# Values under these keys are replaced with "" -- NOT with a placeholder
# string. `"api_key": "<redacted>"` still matches the secret-scan pattern
# `"api_key"\s*:\s*"[^"]+"`, so a placeholder would make our own output trip
# the gate and tempt someone into allowlisting it. "" cannot match `[^"]+`.
SECRET_FIELDS = frozenset({
    "api_key", "apikey", "api_token", "session_secret", "secret", "password",
    "token", "access_token", "refresh_token", "authorization", "auth",
    "private_key", "client_secret",
})

# Free text: replaced by a hash+length stub. Shape (a string of this key is
# present) is what the fixtures are for; the prose is not.
TEXT_FIELDS = frozenset({
    "goal", "content", "note", "reason", "goal_change_reason",
    "reclassify_reason", "spec", "body", "summary", "artifact_summary",
    "description", "text", "message_body", "wrap_note",
})

# Machine identity.
MACHINE_FIELDS = frozenset({
    "machine_id", "machine", "hostname", "host", "machine_name",
})

_HEX64 = re.compile(r"(?<![0-9a-fA-F])[0-9a-fA-F]{64}(?![0-9a-fA-F])")
_SK = re.compile(r"sk-[A-Za-z0-9_\-]{10,}")
_BEARER = re.compile(r"[Bb]earer\s+\S+")
_UUID = re.compile(
    r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b"
)
# Ordered: the longer, more specific path prefixes must win.
_PATH_RULES = (
    (re.compile(r"/root/\.claude"), "<CLAUDE_HOME>"),
    (re.compile(r"/Users/[A-Za-z0-9_.-]+/\.claude"), "<CLAUDE_HOME>"),
    (re.compile(r"/root/code/aicoding/[A-Za-z0-9_.-]+"), "<WORKSPACE>"),
    (re.compile(r"/(?:home|Users)/[A-Za-z0-9_.-]+"), "<HOME>"),
    (re.compile(r"/root"), "<HOME>"),
    (re.compile(r"/var/folders/[A-Za-z0-9_./+-]+"), "<TMP>"),
    (re.compile(r"/tmp/[A-Za-z0-9_./+-]+"), "<TMP>"),
)

MAX_KEPT_STRING = 2000
KEEP_STRING_HEAD = 400


def stub_text(s):
    """Hash+length stub for a redacted free-text field."""
    h = hashlib.sha256(s.encode("utf-8", "replace")).hexdigest()[:12]
    return "<redacted-text sha256=%s len=%d>" % (h, len(s))


def sanitize_text(s):
    """Scrub secrets and machine identity out of one string.

    Every replacement is chosen so the result cannot itself match the
    secret-scan patterns in internal/mcp/corpus_fixture_sanitize_test.go.
    """
    s = _HEX64.sub("<redacted-hex64>", s)
    s = _SK.sub("<redacted-sk>", s)
    s = _BEARER.sub("<redacted-bearer>", s)
    s = _UUID.sub("<uuid>", s)
    for rx, repl in _PATH_RULES:
        s = rx.sub(repl, s)
    return s


def shrink_text(s):
    if len(s) <= MAX_KEPT_STRING:
        return s
    return s[:KEEP_STRING_HEAD] + "<truncated %d chars>" % (len(s) - KEEP_STRING_HEAD)


def sanitize_value(v, key=None, redactions=None):
    """Recursively sanitize a parsed JSON value.

    Field-name rules run first (they are the only way to catch a secret whose
    text has no distinguishing shape), then the textual rules run over
    whatever is left.
    """
    if redactions is None:
        redactions = set()
    lk = key.lower() if isinstance(key, str) else None
    if isinstance(v, dict):
        return {k: sanitize_value(val, k, redactions) for k, val in sorted(v.items())}
    if isinstance(v, list):
        return [sanitize_value(x, key, redactions) for x in v]
    if isinstance(v, str):
        if lk in SECRET_FIELDS:
            redactions.add("field:%s=secret" % lk)
            return ""
        if lk in MACHINE_FIELDS:
            redactions.add("field:%s=machine" % lk)
            return "<machine>"
        if lk in TEXT_FIELDS:
            redactions.add("field:%s=freetext" % lk)
            return stub_text(v)
        return shrink_text(sanitize_text(v))
    return v


# ---------------------------------------------------------------------------
# error classification
# ---------------------------------------------------------------------------

# `aihub 409 CONFLICT_CAS_FAILED: ...`, optionally behind a client prefix such
# as `claim work item: `.
_AIHUB_ERR = re.compile(r"^(?:[^\n:]{0,60}: )?aihub (\d{3}) ([A-Z][A-Z0-9_]+): (.*)$", re.S)
_GUARD_ERR = re.compile(r"^\[polyforge ([a-z0-9-]+)\]\s*(?:[A-Z]+:)?\s*(.*)$", re.S)

_ID_RE = re.compile(r"\b(wi|ra|mem|u|ak|art|dep|proj|stp)_[A-Za-z0-9]{6,}\b")
_SLUG_RE = re.compile(r"\b[a-z][a-z0-9_-]*#\d+\b")
_SHA_RE = re.compile(r"(?<![0-9a-zA-Z])[0-9a-f]{7,40}(?![0-9a-zA-Z])")
_NUM_RE = re.compile(r"(?<![A-Za-z0-9_])\d+(?![A-Za-z0-9_])")
_PATHISH_RE = re.compile(r"(?:<[A-Z_]+>|/)[A-Za-z0-9_.<>-]*(?:/[A-Za-z0-9_.<>-]+)+")
# The commit-guard echoes the offending attribution token into its message.
# An observation pasted into an error string turns into a classification key
# downstream, so it is normalized like any other payload.
_ATTR_TOKEN_RE = re.compile(r'(attribution string )"[^"]*"')


def normalize_message(msg):
    """Strip ids, slugs, hashes, numbers and paths out of an error message.

    Deliberately conservative: it removes only payload that varies per call.
    Two messages differing in a proper noun (a Postgres relation or constraint
    name, for instance) stay on separate rows -- merging those would hide
    which constraint actually fired.
    """
    m = msg.strip()
    m = _ATTR_TOKEN_RE.sub(r'\1"<TOKEN>"', m)
    m = _UUID.sub("<UUID>", m)
    m = _ID_RE.sub(lambda mo: mo.group(1) + "_<ID>", m)
    m = _SLUG_RE.sub("<SLUG>", m)
    for rx, repl in _PATH_RULES:
        m = rx.sub(repl, m)
    m = _PATHISH_RE.sub("<PATH>", m)
    m = _SHA_RE.sub("<SHA>", m)
    m = _NUM_RE.sub("<N>", m)
    m = re.sub(r"\s+", " ", m)
    return m.strip()


def classify_error(text):
    """-> (http_status or '', code, normalized_message)."""
    t = (text or "").strip()
    mo = _AIHUB_ERR.match(t)
    if mo:
        return mo.group(1), mo.group(2), normalize_message(mo.group(3))
    mo = _GUARD_ERR.match(t)
    if mo:
        return "", "CLIENT_" + mo.group(1).upper().replace("-", "_"), normalize_message(mo.group(2))
    return "", "CLIENT_UNCLASSIFIED", normalize_message(t)


# ---------------------------------------------------------------------------
# misc helpers
# ---------------------------------------------------------------------------

def json_type(v):
    if v is None:
        return "null"
    if isinstance(v, bool):
        return "boolean"
    if isinstance(v, int):
        return "integer"
    if isinstance(v, float):
        return "number"
    if isinstance(v, str):
        return "string"
    if isinstance(v, list):
        return "array"
    if isinstance(v, dict):
        return "object"
    return "unknown"


def result_text(block):
    """MCP tool_result.content is either a string or a list of text blocks."""
    c = block.get("content")
    if isinstance(c, str):
        return c
    if isinstance(c, list):
        parts = []
        for b in c:
            if isinstance(b, dict):
                if isinstance(b.get("text"), str):
                    parts.append(b["text"])
            elif isinstance(b, str):
                parts.append(b)
        return "\n".join(parts)
    if c is None:
        return ""
    return json.dumps(c, sort_keys=True)


def parse_body(text):
    """-> (kind, parsed). ~90% of results are strict JSON; the slim renders
    (recall, create, list) are prose and must not be forced."""
    t = (text or "").strip()
    if not t or t[0] not in "[{":
        return "prose", None
    try:
        v = json.loads(t)
    except Exception:
        return "prose", None
    if isinstance(v, dict):
        return "json_object", v
    if isinstance(v, list):
        return "json_array", v
    return "json_scalar", v


def transcript_id(root, path):
    """Stable, non-identifying id. The real path names the machine's home
    directory and the cwd of every session, so it never reaches an artifact."""
    rel = os.path.relpath(path, root)
    return hashlib.sha256(rel.encode("utf-8", "replace")).hexdigest()[:12]


def iso(ts):
    return ts or ""


# ---------------------------------------------------------------------------
# the pass
# ---------------------------------------------------------------------------

class Census:
    def __init__(self):
        self.calls = 0
        self.errors = 0
        self.first = None
        self.last = None
        self.unpaired = 0
        self.out_kinds = Counter()
        self.error_codes = Counter()

    def see(self, ts):
        if not ts:
            return
        if self.first is None or ts < self.first:
            self.first = ts
        if self.last is None or ts > self.last:
            self.last = ts


MAX_PARAM_VALUES = 64
SHOW_VALUES_MAX = 24


class Collector:
    def __init__(self, corpus_root, max_fixture_bytes):
        self.root = corpus_root
        self.max_fixture_bytes = max_fixture_bytes
        self.census = defaultdict(Census)
        self.err_tax = defaultdict(lambda: {"n": 0, "sample": None})
        self.params = defaultdict(lambda: defaultdict(
            lambda: {"n": 0, "types": Counter(), "values": Counter(), "hi_card": False}))
        self.resp_keys = defaultdict(set)
        self.resp_kinds = defaultdict(Counter)
        self.cand = defaultdict(dict)
        self.seq = defaultdict(list)
        self.alias = {}
        self.versions_seen = Counter()
        self.stats = Counter()
        self.ts_fallback = Counter()

    # -- fixture candidates ------------------------------------------------
    def offer(self, rec):
        tool = rec["tool"]
        c = self.cand[tool]
        if rec["is_error"]:
            if "error" not in c:
                c["error"] = rec
        else:
            nparams = len(rec["input"]) if isinstance(rec["input"], dict) else 0
            cur = c.get("happy")
            if cur is None or nparams > cur["_nparams"]:
                rec = dict(rec)
                rec["_nparams"] = nparams
                c["happy"] = rec
        e1, e2 = c.get("edge1"), c.get("edge2")
        if e1 is None or rec["size"] > e1["size"]:
            c["edge1"], c["edge2"] = rec, e1
        elif e2 is None or rec["size"] > e2["size"]:
            c["edge2"] = rec

    # -- one file ----------------------------------------------------------
    def scan_file(self, path):
        try:
            mtime = _dt.datetime.fromtimestamp(
                os.path.getmtime(path), _dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        except OSError:
            mtime = ""
        tid = transcript_id(self.root, path)
        is_sub = os.sep + "subagents" + os.sep in path
        uses = {}
        file_versions = set()
        self.stats["files"] += 1
        try:
            fh = open(path, "rb")
        except OSError:
            self.stats["files_unreadable"] += 1
            return
        with fh:
            for raw in fh:
                self.stats["lines"] += 1
                st = raw.strip()
                if st and not (st.startswith(b"{") and st.endswith(b"}")):
                    self.stats["lines_not_object_shaped"] += 1
                for mo in _PLUGIN_VER_RE.finditer(raw):
                    file_versions.add(mo.group(1).decode())
                has_name = NAME_NEEDLE in raw
                ids = None
                if RESULT_NEEDLE in raw:
                    ids = [m.group(1).decode("utf-8", "replace")
                           for m in _TOOL_USE_ID_RE.finditer(raw)]
                    if not any(i in uses for i in ids):
                        ids = None
                if not has_name and ids is None:
                    continue
                self.stats["lines_parse_attempted"] += 1
                try:
                    obj = json.loads(raw)
                except Exception:
                    self.stats["lines_parse_failed"] += 1
                    continue
                msg = obj.get("message")
                if not isinstance(msg, dict):
                    continue
                content = msg.get("content")
                if not isinstance(content, list):
                    continue
                ts = obj.get("timestamp") or ""
                if not ts:
                    ts = mtime
                    self.ts_fallback["used_file_mtime"] += 1
                for blk in content:
                    if not isinstance(blk, dict):
                        continue
                    bt = blk.get("type")
                    if bt == "tool_use":
                        name = blk.get("name")
                        if not isinstance(name, str) or not name.startswith(TOOL_PREFIX):
                            continue
                        bid = blk.get("id")
                        if not isinstance(bid, str):
                            continue
                        uses[bid] = {
                            "tool": name[len(TOOL_PREFIX):],
                            "input": blk.get("input") if isinstance(blk.get("input"), dict) else {},
                            "ts": ts,
                            "paired": False,
                            "cc_version": obj.get("version") or "",
                        }
                        self.stats["tool_use"] += 1
                    elif bt == "tool_result":
                        rid = blk.get("tool_use_id")
                        u = uses.get(rid) if isinstance(rid, str) else None
                        if u is None or u["paired"]:
                            continue
                        u["paired"] = True
                        self.stats["tool_result_paired"] += 1
                        self.on_pair(tid, is_sub, rid, u, blk, ts)
        for u in uses.values():
            if not u["paired"]:
                self.census[u["tool"]].unpaired += 1
                self.stats["tool_use_unpaired"] += 1
        for v in file_versions:
            self.versions_seen[v] += 1
        if file_versions:
            self.stats["files_with_version_signal"] += 1

    # -- one paired call ---------------------------------------------------
    def on_pair(self, tid, is_sub, rid, u, blk, res_ts):
        tool = u["tool"]
        text = result_text(blk)
        is_error = bool(blk.get("is_error"))
        kind, body = parse_body(text)
        cs = self.census[tool]
        cs.calls += 1
        cs.see(u["ts"])
        cs.out_kinds[kind] += 1
        if is_error:
            cs.errors += 1
        code = ""
        flags = frozenset()
        if is_error:
            status, code, nmsg = classify_error(text)
            if _STATE_MISSING_TEXT_RE.search(text or ""):
                flags = frozenset({"state_missing"})
            cs.error_codes[code] += 1
            key = (tool, status, code, nmsg)
            slot = self.err_tax[key]
            slot["n"] += 1
            if slot["sample"] is None:
                slot["sample"] = shrink_text(sanitize_text(text.strip()))[:300]
        # params
        inp = u["input"]
        for pname, pval in (inp.items() if isinstance(inp, dict) else []):
            p = self.params[tool][pname]
            p["n"] += 1
            p["types"][json_type(pval)] += 1
            if not p["hi_card"]:
                if isinstance(pval, bool) or isinstance(pval, (int, float)):
                    p["values"][json.dumps(pval)] += 1
                elif isinstance(pval, str) and len(pval) <= 48:
                    p["values"][json.dumps(sanitize_text(pval))] += 1
                else:
                    p["values"]["<non-scalar-or-long>"] += 1
                if len(p["values"]) > MAX_PARAM_VALUES:
                    p["hi_card"] = True
                    p["values"].clear()
        # response keys (hop5)
        self.resp_kinds[tool][kind] += 1
        if kind == "json_object" and not is_error:
            self.resp_keys[tool].update(k for k in body.keys() if isinstance(k, str))
        # sequence
        wi_ref = None
        if isinstance(inp, dict):
            for k in ("work_item_id", "wi_id", "slug"):
                v = inp.get(k)
                if isinstance(v, str) and v:
                    wi_ref = v
                    break
        if kind == "json_object" and isinstance(body, dict):
            bid, bslug = body.get("id"), body.get("slug")
            if isinstance(bid, str) and bid.startswith("wi_"):
                self.alias[bid] = bid
                if isinstance(bslug, str) and bslug:
                    self.alias[bslug] = bid
            if wi_ref is None:
                for k in ("work_item_id", "slug"):
                    v = body.get(k)
                    if isinstance(v, str) and v:
                        wi_ref = v
                        break
        if wi_ref:
            self.seq[(tid, wi_ref)].append((u["ts"], tool, code, flags))
        # fixture candidate
        san_in = {}
        redactions = set()
        if isinstance(inp, dict):
            san_in = sanitize_value(inp, None, redactions)
        if kind in ("json_object", "json_array", "json_scalar"):
            san_out = sanitize_value(body, None, redactions)
            out_field = "body"
        else:
            san_out = shrink_text(sanitize_text(text))
            out_field = "text"
        rec = {
            "tool": tool,
            "tool_use_id": rid,
            "ts": u["ts"],
            "transcript_id": tid,
            "is_subagent": is_sub,
            "cc_version": u["cc_version"],
            "input": san_in,
            "raw_input": inp if isinstance(inp, dict) else {},
            "is_error": is_error,
            "error_code": code,
            "out_kind": kind,
            "out_field": out_field,
            "out": san_out,
            "out_full_len": len(text),
            "size": len(json.dumps(inp, default=str)) + len(text),
            "redactions": sorted(redactions),
        }
        self.offer(rec)


# ---------------------------------------------------------------------------
# flow classification
# ---------------------------------------------------------------------------

# Read-only tools: a group made only of these is a lookup, not a flow. Without
# this class 1,058 single-call `pf_get_work_item` lookups landed in
# `unclassified`, which made the residue look like a classifier failure.
READ_ONLY_TOOLS = frozenset({
    "get_work_item", "list_work_items", "get_step", "read_events", "recall",
    "get_memory", "list_projects", "list_users", "list_dependencies",
    "get_ready_queue", "whoami", "predict_conflicts", "diff",
})
WORK_TOOLS = frozenset({"update_step", "save_artifact", "emit_event", "remember"})
SHIP_TOOLS = frozenset({"commit", "pr", "ship", "push"})
TERMINAL_TOOLS = frozenset({"complete_attempt", "wrap"})

FLOW_DETECTORS = (
    ("force_takeover",
     "the group contains a pf_force_takeover call"),
    ("cancel",
     "the group contains a pf_cancel_work_item call"),
    ("pause_resume",
     "a pf_pause_attempt is followed later in the group by a pf_claim_work_item"),
    ("attempt_paused_then_complete",
     "a call fails 409 ATTEMPT_PAUSED and a later pf_complete_attempt/pf_wrap succeeds"),
    ("cas_failure",
     "a call fails with an error code containing CAS_FAILED"),
    ("state_file_missing",
     "a call fails with STATE_NOT_FOUND / NOT_CLAIMED / a missing-state-file client error"),
    ("error_retry",
     "the same tool is called twice in a row, the first failing and the second succeeding"),
    ("commit_guard_block",
     "a pf_commit/pf_ship fails the client-side commit-guard"),
    ("happy_path_completed",
     "claim -> work -> commit/pr/ship/push -> complete/wrap, errors allowed"),
    ("happy_full",
     "the same shape as happy_path_completed, with zero failed calls"),
    ("claim_without_terminal",
     "claim and step/artifact work present, but no complete_attempt/wrap in this "
     "transcript -- work that spans sessions, the commonest real shape"),
    ("terminal_without_claim",
     "complete_attempt/wrap present with no claim in this transcript -- the other "
     "half of a cross-transcript flow"),
    ("lookup_only",
     "every call in the group is a read-only tool: a lookup, not a lifecycle flow"),
)

_STATE_MISSING_RE = re.compile(
    r"STATE_NOT_FOUND|NOT_CLAIMED|STATE_FILE|NO_STATE", re.I)
# Matched against the error TEXT, because the client-side form of this failure
# carries no server error code at all -- it arrives as CLIENT_UNCLASSIFIED with
# the fact only in the prose. Keying on the code alone scored it as 0.
_STATE_MISSING_TEXT_RE = re.compile(
    r"no state file|state file (?:is )?(?:not found|missing|does not exist)"
    r"|not claimed|no claimed attempt|claim it first"
    r"|STATE_NOT_FOUND|NOT_CLAIMED", re.I)


def classify_group(events):
    """events: [(ts, tool, error_code, flags)] already sorted. -> set of labels."""
    tools = [e[1] for e in events]
    codes = [e[2] for e in events]
    flags = [e[3] for e in events]
    labels = set()
    if "force_takeover" in tools:
        labels.add("force_takeover")
    if "cancel_work_item" in tools:
        labels.add("cancel")
    if "pause_attempt" in tools:
        i = tools.index("pause_attempt")
        if "claim_work_item" in tools[i + 1:]:
            labels.add("pause_resume")
    for i, c in enumerate(codes):
        if c == "ATTEMPT_PAUSED":
            for j in range(i + 1, len(events)):
                if tools[j] in ("complete_attempt", "wrap") and not codes[j]:
                    labels.add("attempt_paused_then_complete")
                    break
    if any("CAS_FAILED" in (c or "") for c in codes):
        labels.add("cas_failure")
    if (any(_STATE_MISSING_RE.search(c or "") for c in codes)
            or any("state_missing" in f for f in flags)):
        labels.add("state_file_missing")
    for i in range(len(events) - 1):
        if codes[i] and not codes[i + 1] and tools[i] == tools[i + 1]:
            labels.add("error_retry")
            break
    for i, t in enumerate(tools):
        if t in ("commit", "ship") and codes[i].startswith("CLIENT_COMMIT_GUARD"):
            labels.add("commit_guard_block")
    has_claim = "claim_work_item" in tools
    has_ship = any(t in SHIP_TOOLS for t in tools)
    has_terminal = any(t in TERMINAL_TOOLS for t in tools)
    has_work = any(t in WORK_TOOLS for t in tools)
    if has_claim and has_ship and has_terminal:
        labels.add("happy_path_completed")
        if not any(codes):
            labels.add("happy_full")
    if has_claim and has_work and not has_terminal:
        labels.add("claim_without_terminal")
    if has_terminal and not has_claim:
        labels.add("terminal_without_claim")
    if tools and all(t in READ_ONLY_TOOLS for t in tools):
        labels.add("lookup_only")
    if not labels:
        labels.add("unclassified")
    return labels


def render_trace(events, limit=18):
    parts = []
    for _ts, tool, code, _flags in events[:limit]:
        parts.append(tool + ("(!%s)" % code if code else ""))
    if len(events) > limit:
        parts.append("... +%d more" % (len(events) - limit))
    return " -> ".join(parts)


# ---------------------------------------------------------------------------
# output self-scan
# ---------------------------------------------------------------------------

# The Go gate (internal/mcp/corpus_fixture_sanitize_test.go) scans the committed
# *fixture* tree, which is what guards later hand-edits. It does not reach the
# docs directory, and the docs carry corpus-derived text too: error-message
# samples, observed parameter values. So the producer scans its own output
# before anyone can commit it, over BOTH destinations.
#
# Scoped to the files this run actually wrote. The two hand-written READMEs
# quote these very patterns in order to explain them, and scanning by directory
# instead of by authorship would flag that prose and push the next person toward
# an allowlist.
OUTPUT_SECRET_PATTERNS = (
    ("hex64", re.compile(r"(?<![0-9a-fA-F])[0-9a-fA-F]{64}(?![0-9a-fA-F])")),
    ("sk-token", re.compile(r"sk-[A-Za-z0-9]{10,}")),
    ("bearer", re.compile(r"[Bb]earer\s+\S+")),
    ("api_key-field", re.compile(r'"api_key"\s*:\s*"[^"]+"')),
    ("session_secret-field", re.compile(r'"session_secret"\s*:\s*"[^"]+"')),
    ("absolute-home-path", re.compile(r'/root[/"]|/home/[A-Za-z0-9]|/Users/[A-Za-z0-9]')),
)


def scan_output(paths):
    """-> list of (path, pattern, sample). Empty means clean."""
    found = []
    for p in sorted(paths):
        try:
            with open(p, encoding="utf-8", errors="replace") as fh:
                data = fh.read()
        except OSError:
            continue
        for name, rx in OUTPUT_SECRET_PATTERNS:
            mo = rx.search(data)
            if mo:
                found.append((p, name, mo.group(0)[:80]))
    return found


def self_test():
    """Prove the output scanner can see each shape it claims to check.

    Without this, a clean run is equally consistent with a scanner whose
    patterns stopped matching -- the same reason the Go gate ships a planted
    -secret control rather than only ever pointing at a clean tree.
    """
    import tempfile
    samples = {
        "hex64": '{"s":"9f2c1a4b7e8d0356af91bc2d4e6f80137a5b9c8d2e1f0a3b4c5d6e7f8091a2b3"}',
        "sk-token": '{"k":"sk-ABCdef0123456789xyz"}',
        "bearer": '{"h":"Bearer eyJhbGciOiJIUzI1NiJ9"}',
        "api_key-field": '{"api_key": "pfk_live_not_empty"}',
        "session_secret-field": '{"session_secret": "abc"}',
        "absolute-home-path": '{"p":"/root/.polyforge/state/x.json"}',
    }
    failures = []
    with tempfile.TemporaryDirectory() as d:
        for name, body in samples.items():
            fp = os.path.join(d, name + ".json")
            with open(fp, "w") as fh:
                fh.write(body)
            hits = [h[1] for h in scan_output([fp])]
            if name not in hits:
                failures.append("%s: planted but not detected (got %s)" % (name, hits))
        # and a negative control on the negative control: clean input, no hits
        fp = os.path.join(d, "clean.json")
        with open(fp, "w") as fh:
            fh.write('{"api_key": "", "note": "<redacted-text sha256=abc len=3>"}')
        if scan_output([fp]):
            failures.append("clean sanitized output was flagged: the scanner is too wide")
    for f in failures:
        print("SELF-TEST FAIL: " + f, file=sys.stderr)
    if failures:
        return 1
    print("self-test: %d patterns detect planted secrets; sanitized output stays clean"
          % len(samples))
    return 0


# ---------------------------------------------------------------------------
# writers
# ---------------------------------------------------------------------------

def pct(a, b):
    return "0.00%" if not b else "%.2f%%" % (100.0 * a / b)


def write_census(docs, col, schema_tools, run):
    rows = []
    for tool, cs in col.census.items():
        rows.append({
            "tool": tool,
            "calls": cs.calls,
            "errors": cs.errors,
            "error_rate": pct(cs.errors, cs.calls),
            "first_seen": iso(cs.first),
            "last_seen": iso(cs.last),
            "unpaired_tool_use": cs.unpaired,
            "json_object_results": cs.out_kinds.get("json_object", 0),
            "prose_results": cs.out_kinds.get("prose", 0),
            "published": "yes" if tool in schema_tools else "NO",
        })
    rows.sort(key=lambda r: (-r["calls"], r["tool"]))
    never = sorted(t for t in schema_tools if t not in col.census)
    with open(os.path.join(docs, "tool-census.csv"), "w", newline="") as fh:
        w = csv.DictWriter(fh, fieldnames=list(rows[0].keys()) if rows else ["tool"])
        w.writeheader()
        for r in rows:
            w.writerow(r)
    tot_calls = sum(r["calls"] for r in rows)
    tot_err = sum(r["errors"] for r in rows)
    L = []
    L.append("# aihub#412 - per-tool call census (full corpus)\n")
    L.append("Generated by `scripts/pf_corpus_extract.py`. Do not hand-edit.\n")
    L.append("Run: `%s` | corpus files scanned: %d | JSONL lines: %d\n"
             % (run["generated_at"], run["files"], run["lines"]))
    L.append("")
    L.append("Full census, not a sample: every `*.jsonl` under the corpus root, "
             "subagent transcripts included.\n")
    L.append("**Counted unit is a `tool_use` content block with a "
             "`%s*` name that was paired to its `tool_result` by "
             "`tool_use_id`.** Grepping the tool-name string instead "
             "overcounts by roughly 20x: every session's tool listing repeats all "
             "%d names, so a never-called tool such as `pf_cut_alpha` still shows "
             "thousands of string hits.\n" % (TOOL_PREFIX, len(schema_tools)))
    L.append("")
    L.append("| tool | calls | errors | error rate | first seen | last seen | unpaired | JSON results | prose results | published |")
    L.append("|---|---:|---:|---:|---|---|---:|---:|---:|---|")
    for r in rows:
        L.append("| `pf_%s` | %d | %d | %s | %s | %s | %d | %d | %d | %s |" % (
            r["tool"], r["calls"], r["errors"], r["error_rate"], r["first_seen"],
            r["last_seen"], r["unpaired_tool_use"], r["json_object_results"],
            r["prose_results"], r["published"]))
    L.append("| **total** | **%d** | **%d** | **%s** | | | | | | |"
             % (tot_calls, tot_err, pct(tot_err, tot_calls)))
    L.append("")
    L.append("## Observed / published\n")
    L.append("- tools published by the contract at this SHA: **%d**" % len(schema_tools))
    L.append("- tools with at least one real call: **%d**" % len(col.census))
    L.append("- published but never called: **%d** - %s"
             % (len(never), ", ".join("`pf_%s`" % t for t in never) or "none"))
    unpub = sorted(t for t in col.census if t not in schema_tools)
    L.append("- called but not published at this SHA: **%d** - %s"
             % (len(unpub), ", ".join("`pf_%s`" % t for t in unpub) or "none"))
    L.append("")
    L.append("A never-called tool carries **no frequency argument**: nothing here "
             "says it works, only that no caller in this corpus exercised it.\n")
    with open(os.path.join(docs, "tool-census.md"), "w") as fh:
        fh.write("\n".join(L) + "\n")
    return {"tools_observed": len(col.census), "tools_published": len(schema_tools),
            "never_called": never, "called_not_published": unpub,
            "total_calls": tot_calls, "total_errors": tot_err}


def write_error_taxonomy(docs, col, run):
    items = sorted(col.err_tax.items(), key=lambda kv: (-kv[1]["n"], kv[0]))
    by_code = Counter()
    by_tool = Counter()
    for (tool, status, code, _m), v in items:
        by_code[(status, code)] += v["n"]
        by_tool[tool] += v["n"]
    total = sum(v["n"] for _k, v in items)
    L = []
    L.append("# aihub#412 - normalized error taxonomy\n")
    L.append("Generated by `scripts/pf_corpus_extract.py`. Do not hand-edit.\n")
    L.append("Run: `%s`. Failed calls: **%d** of **%d** (%s).\n"
             % (run["generated_at"], total, run["total_calls"], pct(total, run["total_calls"])))
    L.append("")
    L.append("## Normalization rule\n")
    L.append("A message is keyed on `(tool, http status, error code, normalized message)`. "
             "The normalizer strips only what varies per call: opaque ids "
             "(`wi_*`/`ra_*`/`mem_*`/...), work-item slugs, UUIDs, git SHAs, bare "
             "numbers, filesystem paths, and the attribution token the commit-guard "
             "echoes back. It deliberately does **not** collapse quoted proper nouns, "
             "so two Postgres constraint violations stay on separate rows instead of "
             "merging into one uninformative bucket.\n")
    L.append("")
    L.append("## Level 1 - by (status, code)\n")
    L.append("| http | code | count | share |")
    L.append("|---|---|---:|---:|")
    for (status, code), n in by_code.most_common():
        L.append("| %s | `%s` | %d | %s |" % (status or "-", code, n, pct(n, total)))
    L.append("")
    L.append("## Level 2 - by (tool, status, code, normalized message)\n")
    L.append("| tool | http | code | normalized message | count |")
    L.append("|---|---|---|---|---:|")
    for (tool, status, code, m), v in items:
        mm = m.replace("|", "\\|")
        if len(mm) > 180:
            mm = mm[:177] + "..."
        L.append("| `pf_%s` | %s | `%s` | %s | %d |" % (tool, status or "-", code, mm, v["n"]))
    L.append("")
    L.append("## Failures by tool\n")
    L.append("| tool | failed calls |")
    L.append("|---|---:|")
    for tool, n in by_tool.most_common():
        L.append("| `pf_%s` | %d |" % (tool, n))
    L.append("")
    with open(os.path.join(docs, "error-taxonomy.md"), "w") as fh:
        fh.write("\n".join(L) + "\n")
    return {"distinct_error_rows": len(items), "failed_calls": total,
            "distinct_codes": len(by_code)}


def write_params(docs, col, schema, run):
    L = []
    L.append("# aihub#412 - observed parameters vs published schema\n")
    L.append("Generated by `scripts/pf_corpus_extract.py`. Do not hand-edit.\n")
    L.append("Run: `%s`. Schema contract: `%s`.\n"
             % (run["generated_at"], run["schema_source"]))
    L.append("")
    L.append("## 🔴 Version-alignment caveat - read before calling anything drift\n")
    L.append("The comparison below is **one-sided**: observed traffic spans many "
             "plugin versions, the schema column is a single dump at one SHA. "
             "Per-version dumps do **not** exist -- the `pf-schemas` orphan branch "
             "holds exactly one commit, `%s`, for the same SHA compared here "
             "(verify: `git log --oneline origin/pf-schemas`).\n" % run["schema_source"])
    L.append("")
    L.append("So an **observed-but-not-published** row is a *candidate*, never a "
             "proven regression: the parameter may have been perfectly legal at "
             "send time and removed since. Settling one means finding the plugin "
             "version that sent it and the server schema of that day. "
             "Plugin versions actually witnessed in the corpus (from the "
             "versioned skill path in the transcripts -- the JSONL envelope "
             "carries only the Claude Code version): %s. That signal covers "
             "**%d of %d** transcript files, and the earliest cached plugin "
             "postdates the start of the corpus, so most of the span has no "
             "version signal at all.\n"
             % (", ".join("`%s`" % v for v in run["plugin_versions"]) or "none",
                run["files_with_version_signal"], run["files"]))
    L.append("")
    L.append("A **published-but-never-observed** row is a plain fact about this "
             "corpus and says nothing about correctness.\n")
    L.append("")
    obs_not_pub = []
    pub_not_obs = []
    for tool in sorted(set(list(col.params.keys()) + list(col.census.keys()))):
        sch = schema.get("pf_" + tool, {}).get("params", {})
        obs = col.params.get(tool, {})
        L.append("### `pf_%s`\n" % tool)
        L.append("| param | observed calls | observed JSON types | schema type | required | observed values | status |")
        L.append("|---|---:|---|---|---|---|---|")
        for pname in sorted(set(list(obs.keys()) + list(sch.keys()))):
            o = obs.get(pname)
            s = sch.get(pname)
            n = o["n"] if o else 0
            types = ", ".join("`%s`" % t for t, _ in o["types"].most_common()) if o else "-"
            stype = "`%s`" % s.get("type", "?") if s else "**not published**"
            req = ("yes" if s.get("required") else "no") if s else "-"
            vals = "-"
            if o:
                if o["hi_card"]:
                    vals = "high cardinality (>%d distinct)" % MAX_PARAM_VALUES
                elif len(o["values"]) <= SHOW_VALUES_MAX:
                    vals = ", ".join("`%s`x%d" % (v, c) for v, c in o["values"].most_common())
                    if len(vals) > 160:
                        vals = vals[:157] + "..."
                else:
                    vals = "%d distinct" % len(o["values"])
            if o and not s:
                status = "⚠️ observed, not published at this SHA"
                obs_not_pub.append((tool, pname, n))
            elif s and not o:
                status = "published, never observed"
                pub_not_obs.append((tool, pname))
            else:
                status = "ok"
            L.append("| `%s` | %d | %s | %s | %s | %s | %s |"
                     % (pname, n, types, stype, req, vals.replace("|", "\\|"), status))
        L.append("")
    head = []
    head.append("## Summary\n")
    head.append("- parameters observed but not published at this SHA: **%d**" % len(obs_not_pub))
    for tool, pname, n in sorted(obs_not_pub, key=lambda x: -x[2]):
        head.append("  - `pf_%s.%s` (%d calls)" % (tool, pname, n))
    head.append("- parameters published but never observed: **%d**" % len(pub_not_obs))
    head.append("")
    out = L[:len(L)]
    with open(os.path.join(docs, "param-types-vs-schema.md"), "w") as fh:
        # summary goes above the per-tool tables
        idx = next((i for i, ln in enumerate(out) if ln.startswith("### ")), len(out))
        fh.write("\n".join(out[:idx] + head + out[idx:]) + "\n")
    return {"observed_not_published": len(obs_not_pub),
            "published_not_observed": len(pub_not_obs),
            "observed_not_published_list": ["pf_%s.%s" % (t, p) for t, p, _ in obs_not_pub]}


def write_response_keys(docs, col, written):
    d = os.path.join(docs, "response-keys")
    os.makedirs(d, exist_ok=True)
    for f in os.listdir(d):
        if f.endswith(".json"):
            os.remove(os.path.join(d, f))
    n = 0
    for tool in sorted(col.census.keys()):
        keys = sorted(col.resp_keys.get(tool, ()))
        kinds = col.resp_kinds.get(tool, Counter())
        payload = {
            "tool": "pf_" + tool,
            "purpose": ("hop5 ratchet input: the union of top-level response keys "
                        "any caller has ever been handed. A later projection may not "
                        "drop a key on this list without a deliberate decision."),
            "observed_top_level_keys": keys,
            "result_kinds": dict(sorted(kinds.items())),
            "calls": col.census[tool].calls,
            "note": ("Union over successful strict-JSON object results only. "
                     "Prose (slim-render) results contribute no keys, so a tool "
                     "whose results are all prose has an empty list -- that means "
                     "'not measurable this way', not 'returns nothing'."),
        }
        fp = os.path.join(d, "pf_%s.json" % tool)
        with open(fp, "w") as fh:
            json.dump(payload, fh, indent=2, sort_keys=True)
            fh.write("\n")
        written.append(fp)
        n += 1
    return {"response_key_files": n}


def write_sequences(docs, col, run):
    groups = defaultdict(list)
    for (tid, ref), events in col.seq.items():
        canon = col.alias.get(ref, ref)
        groups[(tid, canon)].extend(events)
    cls = {}
    counts = Counter()
    examples = defaultdict(list)
    for k, events in groups.items():
        events.sort(key=lambda e: (e[0] or "", e[1]))
        labels = classify_group(events)
        cls[k] = labels
        for lb in labels:
            counts[lb] += 1
            if len(examples[lb]) < 3 and len(events) >= 2:
                examples[lb].append((k, events))
    L = []
    L.append("# aihub#412 - real sequence inventory\n")
    L.append("Generated by `scripts/pf_corpus_extract.py`. Do not hand-edit.\n")
    L.append("Run: `%s`.\n" % run["generated_at"])
    L.append("")
    L.append("Calls are grouped per `(transcript, work item)`; a work item referred "
             "to by slug in one call and by id in another is merged through the "
             "slug->id aliases the responses themselves carry. Groups: **%d** over "
             "**%d** distinct work items. Calls that name no work item "
             "(`pf_whoami`, `pf_list_projects`, project-scoped `pf_recall`, ...) form "
             "no group and are excluded.\n"
             % (len(groups), len({c for _t, c in groups})))
    L.append("")
    L.append("Labels are **not** mutually exclusive - one group can be a "
             "force-takeover *and* a CAS failure - so the counts below sum to more "
             "than the group total. These classes are the source for the layer-3 "
             "DB-gated state-machine scenarios (separate wi).\n")
    L.append("")
    szb = Counter()
    unclb = Counter()
    BUCKETS = ((1, 1, "1 call"), (2, 2, "2 calls"), (3, 4, "3-4 calls"),
               (5, 9, "5-9 calls"), (10, 10 ** 9, "10+ calls"))
    for k, events in groups.items():
        n = len(events)
        for lo, hi, name in BUCKETS:
            if lo <= n <= hi:
                szb[name] += 1
                if "unclassified" in cls[k]:
                    unclb[name] += 1
                break
    L.append("## Group size, and what `unclassified` is made of\n")
    L.append("| group size | groups | of which unclassified |")
    L.append("|---|---:|---:|")
    for _lo, _hi, name in BUCKETS:
        L.append("| %s | %d | %d |" % (name, szb[name], unclb[name]))
    L.append("")
    small_uncl = unclb["1 call"] + unclb["2 calls"]
    tot_uncl = sum(unclb.values())
    L.append("Read the `unclassified` row of the next table against this one. "
             "**%d of %d** unclassified groups (%s) are one- or two-call groups, "
             "which are bare references rather than flows the classifier missed; "
             "the remaining **%d** are genuinely unmatched and are the residue "
             "worth looking at. This sentence computes its own share, because an "
             "earlier hand-written version of it said \"the bulk\" and was "
             "falsified the moment a new class absorbed most of the small "
             "groups.\n"
             % (small_uncl, tot_uncl, pct(small_uncl, tot_uncl),
                tot_uncl - small_uncl))
    L.append("")
    L.append("| flow class | groups | share | detector |")
    L.append("|---|---:|---:|---|")
    known = [d[0] for d in FLOW_DETECTORS] + ["unclassified"]
    det = dict(FLOW_DETECTORS)
    det["unclassified"] = "no other detector matched"
    for lb in known:
        L.append("| `%s` | %d | %s | %s |"
                 % (lb, counts.get(lb, 0), pct(counts.get(lb, 0), len(groups)), det[lb]))
    L.append("")
    L.append("A zero row means *this detector found nothing*, which is a claim about "
             "the detector as much as about the corpus; the detector is spelled out "
             "above so it can be falsified.\n")
    L.append("")
    L.append("## Example traces (anonymized)\n")
    L.append("Tool names and error codes only - no payloads. `(!CODE)` marks a "
             "failed call. Transcripts are identified by a hash of their path.\n")
    for lb in known:
        ex = examples.get(lb, [])
        L.append("### `%s` (%d groups)\n" % (lb, counts.get(lb, 0)))
        if not ex:
            L.append("_no group matched this detector_\n")
            continue
        for (tid, canon), events in ex:
            L.append("- transcript `%s`, %d calls\n" % (tid, len(events)))
            L.append("  ```")
            L.append("  " + render_trace(events))
            L.append("  ```")
        L.append("")
    with open(os.path.join(docs, "sequence-inventory.md"), "w") as fh:
        fh.write("\n".join(L) + "\n")
    return {"groups": len(groups), "flow_counts": dict(counts)}


def write_fixtures(fixtures, col, written):
    if os.path.isdir(fixtures):
        for tool in os.listdir(fixtures):
            p = os.path.join(fixtures, tool)
            if os.path.isdir(p):
                for f in os.listdir(p):
                    os.remove(os.path.join(p, f))
                os.rmdir(p)
    os.makedirs(fixtures, exist_ok=True)
    per_tool = {}
    total = 0
    for tool in sorted(col.cand.keys()):
        c = col.cand[tool]
        picks = []
        happy = c.get("happy")
        err = c.get("error")
        if happy:
            picks.append(("happy", happy))
        if err:
            picks.append(("error", err))
        used = {r["tool_use_id"] for _n, r in picks}
        for slot in ("edge1", "edge2"):
            e = c.get(slot)
            if e and e["tool_use_id"] not in used:
                picks.append(("edge", e))
                break
        if not picks:
            continue
        d = os.path.join(fixtures, "pf_" + tool)
        os.makedirs(d, exist_ok=True)
        for role, rec in picks:
            payload = {
                "tool": "pf_" + tool,
                "role": role,
                "provenance": {
                    "transcript_id": rec["transcript_id"],
                    "is_subagent_transcript": rec["is_subagent"],
                    "observed_at": rec["ts"],
                    "claude_code_version": rec["cc_version"],
                    "note": ("Captured from a real call. The transcript is identified "
                             "by a hash of its path: the path itself names the home "
                             "directory and the session cwd."),
                },
                "request": rec["input"],
                "response": {
                    "is_error": rec["is_error"],
                    "error_code": rec["error_code"] or None,
                    "kind": rec["out_kind"],
                    rec["out_field"]: rec["out"],
                    "raw_length_bytes": rec["out_full_len"],
                },
                "sanitization": {
                    "gate": ("internal/mcp/corpus_fixture_sanitize_test.go re-scans "
                             "this tree and fails on any secret shape."),
                    "field_rules_applied": rec["redactions"],
                    "textual_rules": ["hex64", "sk-*", "bearer", "uuid", "machine paths"],
                },
            }
            blob = json.dumps(payload, indent=2, sort_keys=True, ensure_ascii=False)
            if len(blob) > col.max_fixture_bytes:
                payload["response"][rec["out_field"]] = (
                    "<omitted: sanitized response of %d bytes exceeds the "
                    "%d-byte fixture cap; raw_length_bytes records the true size>"
                    % (len(blob), col.max_fixture_bytes))
                payload["response"]["kind"] = rec["out_kind"] + "+capped"
                blob = json.dumps(payload, indent=2, sort_keys=True, ensure_ascii=False)
            fp = os.path.join(d, "%s.json" % role)
            with open(fp, "w") as fh:
                fh.write(blob + "\n")
            written.append(fp)
            total += 1
        per_tool["pf_" + tool] = [r for r, _ in picks]
    return {"fixture_files": total, "fixture_tools": len(per_tool), "per_tool": per_tool}


# ---------------------------------------------------------------------------

def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--corpus", default=os.environ.get("PF_CORPUS_DIR"),
                    help="transcript root (default: $PF_CORPUS_DIR). Never hard-coded.")
    ap.add_argument("--schemas",
                    help="output of `polyforge dump-mcp-schemas <sha>`")
    ap.add_argument("--docs-out")
    ap.add_argument("--fixtures-out")
    ap.add_argument("--max-fixture-bytes", type=int, default=262144)
    ap.add_argument("--self-test", action="store_true",
                    help="prove the output secret-scanner detects each shape, then exit")
    args = ap.parse_args(argv)

    if args.self_test:
        return self_test()

    for req in ("schemas", "docs_out", "fixtures_out"):
        if not getattr(args, req):
            ap.error("--%s is required" % req.replace("_", "-"))
    if not args.corpus:
        ap.error("no corpus root: pass --corpus or set PF_CORPUS_DIR")
    if not os.path.isdir(args.corpus):
        ap.error("corpus root is not a directory: %s" % args.corpus)

    with open(args.schemas) as fh:
        sd = json.load(fh)
    schema = sd.get("tools", {})
    schema_short = {k[3:] for k in schema if k.startswith("pf_")}

    files = []
    for dirpath, _dirnames, filenames in os.walk(args.corpus):
        for fn in filenames:
            if fn.endswith(".jsonl"):
                files.append(os.path.join(dirpath, fn))
    files.sort()

    col = Collector(args.corpus, args.max_fixture_bytes)
    for i, p in enumerate(files):
        col.scan_file(p)
        if (i + 1) % 200 == 0:
            print("  ... %d/%d files" % (i + 1, len(files)), file=sys.stderr, flush=True)

    os.makedirs(args.docs_out, exist_ok=True)
    st = col.stats
    run = {
        "generated_at": _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "schema_source": sd.get("generated_from", "unknown"),
        "files": st["files"],
        "lines": st["lines"],
        "files_with_version_signal": st["files_with_version_signal"],
        "plugin_versions": sorted(col.versions_seen,
                                  key=lambda v: [int(x) for x in v.split(".")]),
        "total_calls": st["tool_result_paired"],
    }
    written = []
    cen = write_census(args.docs_out, col, schema_short, run)
    run["total_calls"] = cen["total_calls"]
    tax = write_error_taxonomy(args.docs_out, col, run)
    par = write_params(args.docs_out, col, schema, run)
    rk = write_response_keys(args.docs_out, col, written)
    seq = write_sequences(args.docs_out, col, run)
    fx = write_fixtures(args.fixtures_out, col, written)
    for name in ("tool-census.md", "tool-census.csv", "error-taxonomy.md",
                 "param-types-vs-schema.md", "sequence-inventory.md"):
        written.append(os.path.join(args.docs_out, name))

    manifest = {
        "generated_at": run["generated_at"],
        "generator": "scripts/pf_corpus_extract.py",
        "schema_contract_sha": run["schema_source"],
        "corpus": {
            "files_scanned": st["files"],
            "files_unreadable": st["files_unreadable"],
            "jsonl_lines": st["lines"],
            "lines_not_object_shaped": st["lines_not_object_shaped"],
            "lines_parse_attempted": st["lines_parse_attempted"],
            "lines_parse_failed": st["lines_parse_failed"],
            "parse_failure_rate_of_attempted": pct(st["lines_parse_failed"],
                                                   st["lines_parse_attempted"]),
            "files_with_plugin_version_signal": st["files_with_version_signal"],
            "plugin_versions_observed": run["plugin_versions"],
        },
        "calls": {
            "tool_use_blocks": st["tool_use"],
            "paired_with_result": st["tool_result_paired"],
            "unpaired_tool_use": st["tool_use_unpaired"],
            "pairing_rate": pct(st["tool_result_paired"], st["tool_use"]),
            "timestamps_from_file_mtime_fallback": col.ts_fallback["used_file_mtime"],
        },
        "census": cen,
        "error_taxonomy": tax,
        "params": par,
        "response_keys": rk,
        "sequences": seq,
        "fixtures": fx,
    }
    mpath = os.path.join(args.docs_out, "run-manifest.json")
    with open(mpath, "w") as fh:
        json.dump(manifest, fh, indent=2, sort_keys=True)
        fh.write("\n")
    written.append(mpath)

    # SANITIZATION IS A GATE: refuse to leave a leaking artifact behind, over
    # every file this run wrote, docs included.
    leaks = scan_output(written)
    if leaks:
        for path, name, sample in leaks:
            print("SECRET LEAK in generated output: %s :: pattern %s :: %r"
                  % (path, name, sample), file=sys.stderr)
        print("refusing to report success: %d leak(s) in %d generated files"
              % (len(leaks), len(written)), file=sys.stderr)
        return 2
    manifest["output_self_scan"] = {
        "files_scanned": len(written),
        "patterns": [n for n, _ in OUTPUT_SECRET_PATTERNS],
        "leaks": 0,
    }
    with open(mpath, "w") as fh:
        json.dump(manifest, fh, indent=2, sort_keys=True)
        fh.write("\n")
    print(json.dumps(manifest, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
