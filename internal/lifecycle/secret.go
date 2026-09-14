package lifecycle

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// StateWriteFilesystemAdvice closes both aihub#323 messages.
//
// It is the third thing a caller needs and the one it is easiest to leave out:
// the two failures the state write can hit are os.MkdirAll and os.WriteFile,
// which do not fail transiently. Without this sentence the "re-run the call"
// advice above reads as a retry loop, and the caller spends its budget bumping
// epochs against a full disk.
const StateWriteFilesystemAdvice = "If the write fails again the fault is this machine's filesystem (disk full, read-only mount, wrong ownership on <workspace>/.polyforge/state) rather than the server, and no number of retries helps until that is fixed."

// recordedClaimSecret returns the session_secret this workspace already recorded
// for (wiID, idemKey), and whether it found one (aihub#392).
//
// ─── Why the idemKey equality is the whole gate ─────────────────────────────
//
// A claim under a NEW key is not a replay: the same-user branch treats it as an
// implicit takeover and INSERTs a fresh attempt bound to the secret that request
// carried. Reusing an older secret there would bind the state file to the wrong
// attempt — the mirror image of the bug being fixed, introduced by over-applying
// its fix. So the recorded secret is reused if and only if the key is the same
// one it was recorded under.
//
// TestE2EClaimWithANewKeyMintsAFreshSecret is the control for exactly that, and
// it is green on the unfixed tree too — it exists to stay green, not to turn.
//
// ⚠️ A PARTIAL stub counts, and that is the point rather than an edge case. The
// C6-2 protocol writes the state file with the chosen secret BEFORE the request,
// so the shape this fix is for — a claim whose response never arrived, retried
// with the same key — is precisely the one where all that survives is a stub with
// claimed=false and no attempt_id. config.ResolveStateFile returns it (it prefers
// a claimed file and falls back to the stub), so both are covered.
//
// Cross-work-item reuse is not reachable: ResolveStateFile matches on the file
// name or on Slug, both of which identify one work item, and the key must match
// on top of that.
func recordedClaimSecret(wiID, idemKey string) (string, bool) {
	if wiID == "" || idemKey == "" {
		return "", false
	}
	sf, err := config.ResolveStateFile(wiID)
	if err != nil || sf == nil {
		return "", false
	}
	if sf.IdemKey != idemKey || sf.SessionSecret == "" {
		return "", false
	}
	return sf.SessionSecret, true
}

// GenerateSessionSecret generates a 64-hex random session secret.
func GenerateSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// resolveWorkspaceConfig returns the workspace config to build claim worktrees
// from, reading .polyforge.yaml fresh out of wsRoot rather than trusting the
// snapshot the calling process loaded at startup (if it loaded one at all).
//
// startupCfg (the MCP server's s.cfg; nil from `polyforge drain`, which loads no
// snapshot at all) is read once when the process starts, so a repo
// added to a project mid-session was silently missing from every subsequent claim
// in that session — the worktree loop iterates this config, and users saw a claim
// come back short without any error (aihub#228).
//
// startupCfg remains the fallback for two cases: the fresh read failing, and the
// server having started without POLYFORGE_WORKSPACE_ROOT (cwd with no
// .polyforge.yaml ancestor), where the snapshot is nil but the caller has since
// resolved a usable wsRoot. Returns nil when neither source yields a config, which
// the caller treats as "skip worktree creation".
//
// ⚠️ For the drain caller startupCfg is ALWAYS nil, so the fallback never applies
// there and the on-disk read is the only source. That is deliberate rather than an
// oversight: a headless run has no session to have loaded a snapshot during.
func resolveWorkspaceConfig(wsRoot string, startupCfg *config.Config) *config.Config {
	if cfg, err := config.Load(wsRoot); err == nil && cfg != nil {
		return cfg
	}
	return startupCfg
}
