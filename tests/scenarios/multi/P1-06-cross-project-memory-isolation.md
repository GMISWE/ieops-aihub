# P1-06 — Cross-project memory: recall is scoped to requested project

## Users
- ALICE_KEY (marketplace/writer)
- ADMIN_KEY (admin, all access)

## Steps

### Alice creates memory in marketplace
AS ALICE: HTTP POST /v1/memories
body: {"project":"marketplace","type":"experience.pitfall","content":"P1-06 marketplace only","visibility":"team"}
Save MEM_ID

### Alice queries project=aihub (no explicit role there)
AS ALICE: HTTP GET /v1/memories?project=aihub&type=experience.*&min_strength=0
ASSERT: HTTP 403 OR (HTTP 200 AND NOT any item.id==MEM_ID)

### Alice queries project=marketplace (her project)
AS ALICE: HTTP GET /v1/memories?project=marketplace&type=experience.*&min_strength=0
ASSERT: HTTP 200, any item.id==MEM_ID

### Admin queries aihub — MEM_ID must not appear there
AS ADMIN: HTTP GET /v1/memories?project=aihub&type=experience.*&min_strength=0
ASSERT: NOT any item.id==MEM_ID

## PASS criteria
Memory scoped to marketplace; not in aihub results.
