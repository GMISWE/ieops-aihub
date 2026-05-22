# P1-04 — visibility=admin memory invisible to writers (server gap detector)

Tests that visibility="admin" memories are not visible to writer-role users.
NOTE: If Bob CAN see the admin memory, this reveals a server bug.

## Users
- ADMIN_KEY (creates admin-only memory)
- BOB_KEY (writer — must NOT see it)

## Steps

### Admin creates admin-only memory
AS ADMIN: HTTP POST /v1/memories
body: {"project":"marketplace","type":"fact.internal","content":"P1-04 ADMIN-ONLY","visibility":"admin"}
ASSERT: HTTP 201, response.visibility=="admin"
Save ADMIN_MEM_ID

### Admin creates project-visible memory (control)
AS ADMIN: HTTP POST /v1/memories
body: {"project":"marketplace","type":"fact.public","content":"P1-04 project visible","visibility":"project"}
Save PROJECT_MEM_ID

### Bob recalls — sees project, must NOT see admin
AS BOB: HTTP GET /v1/memories?project=marketplace&type=fact.*&min_strength=0
ASSERT: any item.id==PROJECT_MEM_ID  (control visible)
ASSERT: NOT any item.id==ADMIN_MEM_ID  <- KEY assertion; FAIL = server bug

### Admin recalls — sees both
AS ADMIN: HTTP GET /v1/memories?project=marketplace&type=fact.*&min_strength=0
ASSERT: any item.id==PROJECT_MEM_ID
ASSERT: any item.id==ADMIN_MEM_ID

## PASS criteria
Bob cannot see admin memory; Admin sees both. FAIL = server visibility bug.
