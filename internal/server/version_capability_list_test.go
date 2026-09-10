package server

// aihub#543 probe wave 1 — the causal half of
// `docs/mcp-cards/pf_update_step.md`'s hop 4 claim about the fused advance:
//
//	"`checkNextStepHonoured` turns an old server's silent drop into a loud
//	 failure by looking for `next_step` echoed in the response — the one
//	 capability signal available, because `GET /v1/version` carries no
//	 capability list."
//
// The first half is held by internal/mcp/tools_fusion_test.go
// (TestFusedUpdateStepDetectsAServerThatDroppedNextStep, and its control
// TestFusedUpdateStepAcceptsAServerThatHonouredNextStep). This file holds the
// SECOND half, which is a claim about this endpoint and not about the client.
//
// 🔴 Why it is worth an arm. The echo check is a workaround whose entire
// justification is that nothing better exists, and the day somebody adds a
// capability or feature list to /v1/version that justification is stale — but
// nothing would fail: the client keeps working, the card keeps saying "no
// capability list", and the next reader takes the workaround for the design.
// This arm makes that addition red, and the failure message says what to do
// (teach the client to read it, and correct the card).
//
// It is written as a CLOSED key set rather than a search for likely names,
// because "carries no capability list" is a claim about the whole payload: a
// scan for the words capability/feature/supports would miss a key named
// `honours_next_step`, which is precisely the field somebody would add.
//
// No database: handleVersion reads no pool.
//
//	GOWORK=off go test ./internal/server/ -run TestVersionPayloadCarriesNoCapabilityList -count=1
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M23 enforcement: add "capabilities": []string{"next_step"} to handleVersion
//	                                        RED  names the new key
//	M24 enforcement: rename min_client_version to min_client
//	                                        RED  the closed-set check catches a
//	                                             rename as well as an addition
//	M25 publication: delete the sentence from the card
//	                                        RED  K12 — candidate and citation
//	                                             leave together

import (
	"sort"
	"testing"
)

// versionPayloadKeys is every key GET /v1/version answers with, and the reason
// each one is not a capability signal.
//
// A map rather than a list so the failure can say what the known keys are FOR.
// The distinction the card rests on is between a build/process identity — which
// tells a client which commit is deployed but nothing about which parameters it
// binds — and a capability list, which would.
var versionPayloadKeys = map[string]string{
	"version":            "the release string; a semver says nothing about which request fields bind",
	"git_commit":         "build identity, and a client cannot map a sha to a parameter set",
	"build_time":         "build identity",
	"started_at":         "process identity (aihub#416's generation component)",
	"min_client_version": "a floor on the CLIENT, which is the opposite direction",
}

func TestVersionPayloadCarriesNoCapabilityList(t *testing.T) {
	payload := versionPayload(t)

	var got []string
	for k := range payload {
		got = append(got, k)
	}
	sort.Strings(got)

	// FLOOR: the payload really rendered. An empty body carries no capability
	// list either, and would pass every assertion below.
	if len(got) < len(versionPayloadKeys) {
		t.Fatalf("GET /v1/version answered %d key(s) %v, and %d are known — a payload smaller than "+
			"the known set means the handler is not rendering, and 'no capability list' would then be "+
			"true of nothing", len(got), got, len(versionPayloadKeys))
	}

	for _, key := range got {
		if _, known := versionPayloadKeys[key]; !known {
			t.Errorf("GET /v1/version now answers %q. If that is a capability, feature or "+
				"parameter-support signal, then internal/mcp/tools_step.go's checkNextStepHonoured is no "+
				"longer working from the only signal available: it should ask the version endpoint "+
				"instead of inferring the peer's binding from an echo, and "+
				"docs/mcp-cards/pf_update_step.md's sentence saying this endpoint carries no capability "+
				"list needs correcting in the same change. If it is only more build or process "+
				"identity, add it to versionPayloadKeys with what it is. Keys: %v", key, got)
		}
	}
	for key, what := range versionPayloadKeys {
		if _, present := payload[key]; !present {
			t.Errorf("GET /v1/version no longer answers %q (%s), so this arm is measuring a payload "+
				"that has changed shape; check whether what replaced it is a capability signal before "+
				"editing the expected set. Keys: %v", key, what, got)
		}
	}
}
