# L3-03 — Memory-First: recall before claiming returns relevant history

Tests that pf_recall surfaces previously saved experience before claiming a similar wi,
and that pf_activate_memory increments activation_count.

## Setup — seed a historical memory

CALL: pf_remember(project="marketplace", type="experience.debug",
      content="CSS variable approach for theming: use :root vars + data-theme attribute. Pitfall: flash-of-unstyled-content on reload — add <script> in <head> to apply theme before paint.",
      visibility="project")
ASSERT: response.is_new == true
NOTE: save response.id as HIST_MEM_ID

## Steps

### Recall before claiming a related wi
CALL: pf_recall(project="marketplace",
      query="dark mode CSS theming pitfalls",
      type=["experience.*"],
      top_k="3",
      min_strength=0.1)
ASSERT: len(response.items) >= 1
ASSERT: any item.id == HIST_MEM_ID
NOTE: save response.items[0].effective_strength as STRENGTH_BEFORE

### Activate the relevant memory
CALL: pf_activate_memory(memory_id=HIST_MEM_ID)
ASSERT: response.activation_count >= 1

### Verify activation_count incremented
CALL: pf_recall(project="marketplace", type=["experience.*"], top_k="5")
NOTE: find item where id == HIST_MEM_ID
ASSERT: item.activation_count >= 1

### Create wi referencing the memory
CALL: pf_create_work_item(project="marketplace",
      goal="[test] L3-03 fix CSS flash on dark mode reload",
      wi_type="fix_bug", priority="normal")
NOTE: save response.id as WI_ID

CALL: pf_claim_work_item(work_item_id=WI_ID, idempotency_key="l3-03-claim", mode="fresh")
ASSERT: response.ok == true

### Save experience from this wi (related to historical memory)
CALL: pf_remember(project="marketplace", type="experience.debug",
      content="L3-03 confirmed: add theme-init.js to <head> eliminates FOUC. 10ms overhead acceptable.",
      visibility="project", work_item_id=WI_ID)
ASSERT: response.is_new == true
NOTE: save response.id as NEW_MEM_ID

### Recall again — should return both memories
CALL: pf_recall(project="marketplace", query="CSS dark mode flash", type=["experience.*"], top_k="5")
ASSERT: len(response.items) >= 2

## Cleanup

CALL: pf_complete_attempt(work_item_id=WI_ID, status="wrapped")
ASSERT: response.ok == true

## PASS criteria

Historical memory recalled before claiming; activation_count incremented;
new wi's memory added; both recallable in subsequent query.
