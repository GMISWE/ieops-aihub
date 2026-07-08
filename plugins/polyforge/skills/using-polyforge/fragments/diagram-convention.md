## Diagrams: author as d2 (aihub renders to SVG)

When an artifact destined for aihub contains a diagram, author it as a fenced ` ```d2 `
block in **D2** syntax. aihub's `/ui` artifact viewer compiles ` ```d2 ` blocks to inline
SVG server-side (aihub#160). Other diagram syntaxes (mermaid, etc.) are **not** rendered —
they stay as plain code blocks.

This applies to every artifact that lands in aihub: `/pf-spec` and `/pf-plan` output, and any
`methodology.*` artifact.

- Use D2 syntax (`a -> b: label`, `node: { ... }`), not mermaid.
- Rendering is `/ui`-only; `/v1` + `/share` keep the raw fenced block (byte-stable).
- A d2 block that fails to compile degrades gracefully back to a code block.
