# aihub#240 — P0 spike artifact

## What this artifact is

This is the P0 spike artifact for aihub#240. It exists to prove the three-step render architecture end to end on one real document class (an architecture doc), not to be read as product documentation.

It is produced as a twin pair: this markdown is the editable source, and the HTML beside it is the finished page the viewer actually renders. Both are agent-authored, which is exactly why a consistency check between them is required rather than optional.

## Pipeline

The write path lays figures out once and stores static SVG. The read path compiles nothing at all, which is what removes the entire class of intermittent first-render failures the old runtime D2 path suffered from.

```d2
direction: right
write: "write path" {
  agent: "agent produces\nmd + html"
  check: "consistency check\nmd <-> html"
  freeze: "complex figure:\nDSL -> layout -> frozen SVG"
  san: "sanitize agent content\n(SVG policy, DTD, drops <style>)"
  agent -> check
  check -> san: "prose + simple SVG"
  check -> freeze: "complex figure DSL"
}
insert: "insert frozen SVG\nafter sanitizing" {shape: diamond}
store: "memories.rendered_html" {shape: cylinder}
read: "read path (zero compile)" {
  embed: "srcdoc + sandbox\nallow-scripts"
  csp: "CSP header\n/ui + /v1"
  bridge: "annotation bridge\npostMessage"
  embed -> csp
  embed -> bridge
}
reader: "project member" {shape: person}
write.san -> insert
write.freeze -> insert: "trusted, never sanitized"
insert -> store
store -> read.embed
read.csp -> reader
read.bridge -> reader: select / highlight
```

## Two diagram tracks

Simple figures are written directly as inline SVG by the agent. Complex figures are not: an LLM cannot place coordinates reliably at that density, so the agent emits a structured DSL and a deterministic layout engine positions it, once, at write time.

Both tracks appear in this artifact so the spike covers the branch it actually has to support, rather than passing on the easy case alone.

Simple track — hand-written inline SVG (three-step flow).

Complex track — DSL above, frozen to static SVG at write time.

## Security posture

Agent-authored HTML is untrusted rich content. Three independent layers apply, and none of them is assumed sufficient on its own: server-side sanitization strips script, event handlers, javascript: URIs, external resource references and XML DTD declarations; the document is displayed inside an iframe sandboxed without allow-same-origin; and the response carries a Content-Security-Policy.

The figure below is at the density this architecture has to survive. If sanitization or the sandbox clipped gradients, filters or clip paths, it would be visibly wrong here.

Figure: dense multi-node architecture diagram (23 nodes, 25 edges, gradients, drop-shadow and blur filters, a clip path, and cubic-bezier edge routing).

## What is verified where

Unit tests cover sanitization, embedding, the bridge's configuration surface and the freeze guardrails. Browser behaviour is not claimed by them: script non-execution, sandbox escape attempts, CSP enforcement in both directions, undistorted rendering of the dense figure, and the annotation round trip are verified by deploying to aihub-test and running the checklist against a real browser.
