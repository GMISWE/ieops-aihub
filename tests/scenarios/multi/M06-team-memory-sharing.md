# M06 — Team memory sharing: Alice writes, Bob reads, Carol sees public only

Real-world scenario: Alice (agent) discovers a pitfall during execution and saves
a team-visible memory. Bob (writer) later recalls it before starting similar work.
Carol (viewer) can recall team-visible memories but not private ones.

## Users
- ALICE_KEY=pf_k1_H36gVOed7wzTH4cPA1FpsG37qsia117V  (Test Agent Alice, writer)
- BOB_KEY=pf_k1_NekUaAWXMdZf5WVfrpdmd7V8d1NVn1VR  (Test Writer Bob, writer)
- CAROL_KEY=pf_k1_2j5gcKsUTBRazaEWydEQ1i4bDRwdR6Bh  (Test Viewer Carol, viewer)
- ADMIN_KEY=baOHJg3Gh7JMpV5kW2Q1BHPqweg3y5Ig  (admin)

## Steps

### Alice: save a team-visible experience (pitfall she discovered)
AS ALICE:
HTTP POST /v1/memories
body (as Alice): {"project":"marketplace","type":"experience.pitfall",
                  "content":"M06 test: When updating auth middleware, always run integration tests first. Unit tests pass but wire-level token validation can silently fail.",
                  "visibility":"team"}
ASSERT: HTTP 201
ASSERT: response.visibility == "team"
NOTE: save response.id as PITFALL_MEM_ID

### Alice: also save a private memory (only Alice sees this)
AS ALICE:
HTTP POST /v1/memories
body: {"project":"marketplace","type":"experience.note",
       "content":"M06 test: Private note — only Alice should see this.",
       "visibility":"private"}
ASSERT: HTTP 201
NOTE: save response.id as PRIVATE_MEM_ID

### Bob: recall before starting similar work — should see Alice's team memory
AS BOB:
HTTP GET /v1/memories?project=marketplace&type=experience.*&query=auth+middleware
ASSERT: HTTP 200
ASSERT: any(mem for mem in response.items if mem.id == PITFALL_MEM_ID)
NOTE: Bob sees Alice's team memory

### Bob: should NOT see Alice's private memory
AS BOB:
HTTP GET /v1/memories?project=marketplace&type=experience.*
ASSERT: not any(mem for mem in response.items if mem.id == PRIVATE_MEM_ID)

### Carol: viewer can also see team-visible memory
AS CAROL:
HTTP GET /v1/memories?project=marketplace&type=experience.pitfall
ASSERT: HTTP 200
ASSERT: any(mem for mem in response.items if mem.id == PITFALL_MEM_ID)

### Carol: cannot write memories (viewer on project)
AS CAROL:
HTTP POST /v1/memories
body: {"project":"marketplace","type":"experience.note",
       "content":"M06 test: Carol tries to write","visibility":"project"}
ASSERT_ERROR: HTTP 403

### Bob: activate the pitfall memory (acknowledging it's useful)
AS BOB:
HTTP POST /v1/memories/PITFALL_MEM_ID/activate
ASSERT: HTTP 200
ASSERT: response.activation_count >= 1

### Alice: sees her memory activation_count increased
AS ALICE:
HTTP GET /v1/memories?project=marketplace&type=experience.pitfall
NOTE: find PITFALL_MEM_ID in results
ASSERT: item.activation_count >= 1

### Bob: save a project-visible response memory (shares his experience)
AS BOB:
HTTP POST /v1/memories
body: {"project":"marketplace","type":"experience.debug",
       "content":"M06 test: Confirmed Alice's pitfall — fixed by running `make test-integration` before push.",
       "visibility":"project"}
ASSERT: HTTP 201
NOTE: save response.id as BOB_MEM_ID

### Alice: can see Bob's project memory
AS ALICE:
HTTP GET /v1/memories?project=marketplace&type=experience.debug
ASSERT: any(mem for mem in response.items if mem.id == BOB_MEM_ID)

## Cleanup

No wi's created — no cleanup needed.

## PASS criteria

Alice's team memory visible to Bob and Carol; Alice's private memory invisible to Bob/Carol;
Carol cannot write; Bob can activate and create project memories; Alice sees Bob's memory.
