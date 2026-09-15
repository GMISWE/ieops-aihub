package config

import (
	"os"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestResolveAPIKey verifies the credential precedence contract documented in
// `polyforge --help`: POLYFORGE_API_KEY (highest priority) > config.toml
// [auth] api_key > the env var named by [auth] api_key_env.
func TestResolveAPIKey(t *testing.T) {
	const customEnvName = "MY_CUSTOM_KEY_ENV"

	tests := []struct {
		name      string
		apiKey    string // config.toml [auth] api_key
		apiKeyEnv string // config.toml [auth] api_key_env (env var name)
		envGlobal string // POLYFORGE_API_KEY value ("" = unset)
		envCustom string // value of the customEnvName env var ("" = unset)
		want      string
	}{
		{
			name:      "env-only: POLYFORGE_API_KEY with empty config",
			envGlobal: "env-key",
			want:      "env-key",
		},
		{
			name:   "config-only: api_key with no env set",
			apiKey: "config-key",
			want:   "config-key",
		},
		{
			name:      "env-overrides-config: POLYFORGE_API_KEY wins over api_key",
			apiKey:    "config-key",
			envGlobal: "env-key",
			want:      "env-key",
		},
		{
			name:      "api_key_env indirection used when global env and api_key empty",
			apiKeyEnv: customEnvName,
			envCustom: "indirect-key",
			want:      "indirect-key",
		},
		{
			name:      "global POLYFORGE_API_KEY wins over api_key_env indirection",
			apiKeyEnv: customEnvName,
			envCustom: "indirect-key",
			envGlobal: "env-key",
			want:      "env-key",
		},
		{
			name:      "config api_key wins over api_key_env indirection",
			apiKey:    "config-key",
			apiKeyEnv: customEnvName,
			envCustom: "indirect-key",
			want:      "config-key",
		},
		{
			name: "nothing set returns empty",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv isolates and restores env per subtest.
			t.Setenv("POLYFORGE_API_KEY", tt.envGlobal)
			t.Setenv(customEnvName, tt.envCustom)

			mc := &MachineConfig{
				Auth: MachineAuth{
					APIKey:    tt.apiKey,
					APIKeyEnv: tt.apiKeyEnv,
				},
			}
			if got := mc.ResolveAPIKey(); got != tt.want {
				t.Errorf("ResolveAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveAihubURL verifies POLYFORGE_AIHUB_URL (override) wins over the
// config.toml [server] url, matching the `--help` "override" wording.
func TestResolveAihubURL(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string // config.toml [server] url ("" = no [server] block)
		envURL    string // POLYFORGE_AIHUB_URL value ("" = unset)
		want      string
	}{
		{
			name:   "env-only: POLYFORGE_AIHUB_URL with no config",
			envURL: "http://env.example",
			want:   "http://env.example",
		},
		{
			name:      "config-only: [server] url with no env set",
			serverURL: "http://config.example",
			want:      "http://config.example",
		},
		{
			name:      "env-overrides-config: POLYFORGE_AIHUB_URL wins",
			serverURL: "http://config.example",
			envURL:    "http://env.example",
			want:      "http://env.example",
		},
		{
			name: "nothing set returns empty",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("POLYFORGE_AIHUB_URL", tt.envURL)

			mc := &MachineConfig{}
			if tt.serverURL != "" {
				mc.Server = &MachineServer{URL: tt.serverURL}
			}
			if got := mc.ResolveAihubURL(); got != tt.want {
				t.Errorf("ResolveAihubURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveBinaryChannel pins the aihub#305 contract: `dev` is the only
// published bins-<channel> branch, so it is the default AND the answer for the
// legacy `stable` value that is still sitting in existing config.toml files.
//
// This helper has no callers today — it is the Go-side twin of the case
// statement in plugins/polyforge/bin/polyforge-mcp.sh, which is what actually
// downloads the binary. That is precisely why it is worth pinning: an
// uncalled twin drifts silently, and whoever wires it up later inherits
// whatever it happens to say. The launcher's own behaviour is covered by
// plugins/polyforge/tests/launcher-channel-url.test.sh, which additionally
// fetches the resolved URL — a string check cannot tell a published branch
// from a 404, which is how "stable" survived as the default for months.
func TestResolveBinaryChannel(t *testing.T) {
	tests := []struct {
		name    string
		binary  *MachineBinary
		want    string
		comment string
	}{
		{
			name: "no [binary] section at all defaults to dev",
			want: "dev",
		},
		{
			name:   "empty channel defaults to dev",
			binary: &MachineBinary{Channel: ""},
			want:   "dev",
		},
		{
			name:   "explicit dev is honoured",
			binary: &MachineBinary{Channel: "dev"},
			want:   "dev",
		},
		{
			name:    "legacy stable maps onto dev rather than a 404",
			binary:  &MachineBinary{Channel: "stable"},
			want:    "dev",
			comment: "bins-stable was never published; returning it verbatim resolves to a 404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MachineConfig{Binary: tt.binary}
			if got := mc.ResolveBinaryChannel(); got != tt.want {
				t.Errorf("ResolveBinaryChannel() = %q, want %q (%s)", got, tt.want, tt.comment)
			}
		})
	}
}

// TestEffectiveAihubURL pins the four-layer precedence and, more importantly,
// that the last layer EXISTS. aihub#335 removed the address from
// docs/onboarding.md's config.toml heredoc on the strength of this function
// answering for a machine that configured nothing; if the default layer is ever
// dropped, that document becomes a guide with no way to reach the server and
// nothing else in the tree would notice.
func TestEffectiveAihubURL(t *testing.T) {
	tests := []struct {
		name       string
		serverURL  string // config.toml [server] url ("" = no [server] block)
		envURL     string // POLYFORGE_AIHUB_URL ("" = unset)
		wsURL      string // .polyforge.yaml aihub.url
		want       string
		wantSource string
	}{
		{
			name:       "nothing configured falls back to the built-in default",
			want:       AihubURLDefault,
			wantSource: "built-in default",
		},
		{
			name:       "workspace url beats the default",
			wsURL:      "http://ws.example",
			want:       "http://ws.example",
			wantSource: ".polyforge.yaml aihub.url",
		},
		{
			name:       "config.toml beats the workspace",
			serverURL:  "http://config.example",
			wsURL:      "http://ws.example",
			want:       "http://config.example",
			wantSource: "~/.polyforge/config.toml [server] url",
		},
		{
			name:       "env beats everything",
			serverURL:  "http://config.example",
			wsURL:      "http://ws.example",
			envURL:     "http://env.example",
			want:       "http://env.example",
			wantSource: "POLYFORGE_AIHUB_URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("POLYFORGE_AIHUB_URL", tt.envURL)

			mc := &MachineConfig{}
			if tt.serverURL != "" {
				mc.Server = &MachineServer{URL: tt.serverURL}
			}
			got, source := EffectiveAihubURL(mc, tt.wsURL)
			if got != tt.want {
				t.Errorf("EffectiveAihubURL() url = %q, want %q", got, tt.want)
			}
			if source != tt.wantSource {
				t.Errorf("EffectiveAihubURL() source = %q, want %q", source, tt.wantSource)
			}
		})
	}
}

// TestEffectiveAihubURLToleratesANilMachineConfig covers doctor.go's caller,
// which reaches this function with whatever LoadMachineConfig returned and
// ignores the error — a config.toml that does not parse yields (nil, err), and
// a diagnostic command must not panic on the machine it was run to diagnose.
func TestEffectiveAihubURLToleratesANilMachineConfig(t *testing.T) {
	t.Setenv("POLYFORGE_AIHUB_URL", "")

	got, source := EffectiveAihubURL(nil, "")
	if got != AihubURLDefault || source != "built-in default" {
		t.Errorf("EffectiveAihubURL(nil, \"\") = (%q, %q), want (%q, %q)",
			got, source, AihubURLDefault, "built-in default")
	}
}

// TestResolveAihubURLStillReportsAnUnconfiguredMachineAsEmpty guards the
// distinction EffectiveAihubURL's doc comment depends on. writePolyforgeYAML
// copies ResolveAihubURL's result into .polyforge.yaml; if this ever started
// returning the built-in default, every workspace would get a hard copy of
// today's address in a file no release can update — the same defect aihub#335
// removed from the documents, re-created one file per workspace.
func TestResolveAihubURLStillReportsAnUnconfiguredMachineAsEmpty(t *testing.T) {
	t.Setenv("POLYFORGE_AIHUB_URL", "")

	if got := (&MachineConfig{}).ResolveAihubURL(); got != "" {
		t.Errorf("ResolveAihubURL() on an unconfigured machine = %q, want \"\" — see "+
			"EffectiveAihubURL's comment: this value gets PERSISTED into .polyforge.yaml", got)
	}
}

// TestMachineConfigRolesOmittedWhenUnset pins the "no [roles] table at all for a
// machine that never touches it" contract (aihub#642): Roles is a pointer with
// omitempty specifically so a config.toml written before this field existed, or
// by a machine that never sets it, marshals with no [roles] section and
// round-trips through Unmarshal with Roles == nil rather than a non-nil empty
// struct.
func TestMachineConfigRolesOmittedWhenUnset(t *testing.T) {
	mc := &MachineConfig{MachineID: "m-1"}
	b, err := toml.Marshal(mc)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if got := string(b); containsRolesTable(got) {
		t.Errorf("Marshal() with unset Roles produced a [roles] table:\n%s", got)
	}

	var round MachineConfig
	if err := toml.Unmarshal(b, &round); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if round.Roles != nil {
		t.Errorf("round-tripped Roles = %+v, want nil", round.Roles)
	}
}

func containsRolesTable(s string) bool {
	return len(s) >= len("[roles]") && (indexOf(s, "[roles]") >= 0 || indexOf(s, "[roles.tiers") >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestMachineConfigRolesRoundTrip pins aihub#642 mem_leO2mZmw decision #1's
// shape: [roles.tiers] maps a tier name to an ORDERED list of (harness, model)
// candidates, and both the ordering and the harness/model pairing survive a
// Marshal -> Unmarshal round trip byte-for-byte in the decoded struct.
func TestMachineConfigRolesRoundTrip(t *testing.T) {
	mc := &MachineConfig{
		MachineID: "m-1",
		Roles: &MachineRoles{
			Tiers: map[string][]RoleCandidate{
				"default": {
					{Harness: "pi", Model: "claude-sonnet-4-5"},
					{Harness: "codex", Model: "gpt-5-codex"},
				},
				"raised": {
					{Harness: "pi", Model: "claude-opus-4-1"},
				},
			},
		},
	}

	b, err := toml.Marshal(mc)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var round MachineConfig
	if err := toml.Unmarshal(b, &round); err != nil {
		t.Fatalf("Unmarshal() error: %v\ntoml:\n%s", err, b)
	}
	if round.Roles == nil {
		t.Fatalf("round-tripped Roles is nil; marshaled toml:\n%s", b)
	}
	if !reflect.DeepEqual(round.Roles.Tiers["default"], mc.Roles.Tiers["default"]) {
		t.Errorf("round-tripped Tiers[default] = %+v, want %+v (order must survive)",
			round.Roles.Tiers["default"], mc.Roles.Tiers["default"])
	}
	if !reflect.DeepEqual(round.Roles.Tiers["raised"], mc.Roles.Tiers["raised"]) {
		t.Errorf("round-tripped Tiers[raised] = %+v, want %+v",
			round.Roles.Tiers["raised"], mc.Roles.Tiers["raised"])
	}
	if len(round.Roles.Tiers) != 2 {
		t.Errorf("round-tripped Tiers has %d keys, want 2: %+v", len(round.Roles.Tiers), round.Roles.Tiers)
	}
}

// TestSaveMachineConfigRolesExample pins that SaveMachineConfig's footer
// documents the [roles.tiers] table (so a reader of a fresh config.toml can
// discover the feature without leaving the file), without asserting on the
// exact prose so wording can still be improved freely.
func TestSaveMachineConfigRolesExample(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveMachineConfig(&MachineConfig{MachineID: "m-1"}); err != nil {
		t.Fatalf("SaveMachineConfig() error: %v", err)
	}
	b, err := os.ReadFile(MachineConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if indexOf(string(b), "roles.tiers") < 0 {
		t.Errorf("SaveMachineConfig() footer does not mention roles.tiers; got:\n%s", b)
	}
}

// ─── aihub#673: tier-table presets ────────────────────────────────────────────

// TestResolveTiersPrecedence pins the one rule that makes a preset mean anything:
// an explicit --preset beats the machine's [roles] preset, which beats the bare
// [roles.tiers] table.
//
// The precedence is the whole contract. `polyforge roles generate`, the
// serve-startup codex profile generation and `polyforge drain` all resolve
// through this function precisely so a machine cannot answer "which models do I
// use" differently depending on which one was asked, so an ordering defect here
// is not a local bug -- it is the two-口径 hazard aihub#673 exists to close,
// reintroduced one level down.
func TestResolveTiersPrecedence(t *testing.T) {
	bare := []RoleCandidate{{Harness: "pi", Model: "bare-table"}}
	frugal := []RoleCandidate{{Harness: "pi", Model: "frugal-model"}}
	maxp := []RoleCandidate{{Harness: "pi", Model: "max-model"}}

	full := func() *MachineConfig {
		return &MachineConfig{Roles: &MachineRoles{
			Tiers:  map[string][]RoleCandidate{"default": bare},
			Preset: "frugal",
			Presets: map[string]*RolesPreset{
				"frugal": {Tiers: map[string][]RoleCandidate{"default": frugal}},
				"max":    {Tiers: map[string][]RoleCandidate{"default": maxp}},
			},
		}}
	}

	tests := []struct {
		name      string
		mc        *MachineConfig
		override  string
		wantModel string // "" means "no table resolved"
	}{
		{
			name:      "override beats the configured preset",
			mc:        full(),
			override:  "max",
			wantModel: "max-model",
		},
		{
			name:      "configured preset beats the bare table",
			mc:        full(),
			override:  "",
			wantModel: "frugal-model",
		},
		{
			name: "bare table is used when no preset is selected",
			mc: &MachineConfig{Roles: &MachineRoles{
				Tiers: map[string][]RoleCandidate{"default": bare},
			}},
			override:  "",
			wantModel: "bare-table",
		},
		{
			name: "an override still wins when there is no configured preset",
			mc: &MachineConfig{Roles: &MachineRoles{
				Tiers:   map[string][]RoleCandidate{"default": bare},
				Presets: map[string]*RolesPreset{"max": {Tiers: map[string][]RoleCandidate{"default": maxp}}},
			}},
			override:  "max",
			wantModel: "max-model",
		},
		{
			name:      "a machine that configured nothing resolves no table",
			mc:        &MachineConfig{},
			override:  "",
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tiers, source, err := tt.mc.ResolveTiers(tt.override)
			if err != nil {
				t.Fatalf("ResolveTiers(%q) unexpected error: %v", tt.override, err)
			}
			if source == "" {
				t.Errorf("ResolveTiers(%q) returned an empty source; every caller prints it "+
					"so an operator can see which table produced the result", tt.override)
			}
			var got string
			if len(tiers["default"]) > 0 {
				got = tiers["default"][0].Model
			}
			if got != tt.wantModel {
				t.Errorf("ResolveTiers(%q) default tier model = %q, want %q (source=%q)",
					tt.override, got, tt.wantModel, source)
			}
		})
	}
}

// TestResolveTiersRefusesUnknownPreset pins that an unresolvable preset name is
// an ERROR, not a quiet fall back to [roles.tiers].
//
// This is the single most important assertion in this file. Falling back would
// be invisible in every output the operator sees: they ask for `frugal`, the
// binary runs the default table, nothing disagrees with them, and the bill
// arrives later. A refusal costs one typo-correction; a silent substitution can
// run for weeks. The test therefore also requires the message to NAME the
// available presets, because "unknown preset" on its own does not tell somebody
// whether they misspelled the name or never defined it.
func TestResolveTiersRefusesUnknownPreset(t *testing.T) {
	mc := &MachineConfig{Roles: &MachineRoles{
		Tiers: map[string][]RoleCandidate{"default": {{Harness: "pi", Model: "bare-table"}}},
		Presets: map[string]*RolesPreset{
			"frugal": {Tiers: map[string][]RoleCandidate{"default": {{Harness: "pi", Model: "cheap"}}}},
			"max":    {Tiers: map[string][]RoleCandidate{"default": {{Harness: "pi", Model: "dear"}}}},
		},
	}}

	tiers, _, err := mc.ResolveTiers("frugl") // a plausible typo, not nonsense
	if err == nil {
		t.Fatalf("ResolveTiers(\"frugl\") returned no error; it must refuse rather than fall back "+
			"to [roles.tiers] (got tiers=%+v)", tiers)
	}
	if tiers != nil {
		t.Errorf("ResolveTiers on an unknown preset returned a table (%+v); it must return none, "+
			"or a caller that only checks the table will run the wrong models", tiers)
	}
	for _, want := range []string{"frugl", "frugal", "max"} {
		if !contains(err.Error(), want) {
			t.Errorf("ResolveTiers error %q does not mention %q; it must name both the bad name "+
				"and the available ones", err, want)
		}
	}
}

// TestResolveTiersRefusesPresetWithNoTiers pins that a declared-but-empty preset
// is refused too. Resolving it to an empty table would silently discard
// [roles.tiers] as well and leave every role inheriting its harness's default
// model -- a quieter version of the same failure as an unknown name.
func TestResolveTiersRefusesPresetWithNoTiers(t *testing.T) {
	mc := &MachineConfig{Roles: &MachineRoles{
		Tiers:   map[string][]RoleCandidate{"default": {{Harness: "pi", Model: "bare-table"}}},
		Presets: map[string]*RolesPreset{"hollow": {}},
	}}

	if _, _, err := mc.ResolveTiers("hollow"); err == nil {
		t.Fatal("ResolveTiers on a preset that declares no tiers returned no error; " +
			"an empty table would silently discard [roles.tiers] too")
	}
}

// TestResolveTiersNilReceiverAndNilRoles pins that the three "configured
// nothing" shapes are answers rather than crashes. A nil *MachineConfig reaches
// this function on the path runCLI takes when config.toml does not parse.
func TestResolveTiersNilReceiverAndNilRoles(t *testing.T) {
	var nilMC *MachineConfig
	for _, tc := range []struct {
		name string
		mc   *MachineConfig
	}{
		{"nil receiver", nilMC},
		{"nil Roles", &MachineConfig{MachineID: "m-1"}},
		{"empty Roles", &MachineConfig{Roles: &MachineRoles{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tiers, source, err := tc.mc.ResolveTiers("")
			if err != nil {
				t.Fatalf("ResolveTiers(\"\") error: %v", err)
			}
			if len(tiers) != 0 {
				t.Errorf("ResolveTiers(\"\") = %+v, want an empty table", tiers)
			}
			if source == "" {
				t.Error("ResolveTiers(\"\") returned an empty source string")
			}
		})
	}
	// A preset asked for on a machine that defines none must still refuse, not
	// return the empty table as though the request had been honoured.
	if _, _, err := nilMC.ResolveTiers("frugal"); err == nil {
		t.Error("ResolveTiers(\"frugal\") on a nil config returned no error; " +
			"a requested preset that cannot exist must be refused")
	}
}

// TestMachineConfigPresetsRoundTrip pins that presets survive Marshal ->
// Unmarshal with their candidate ORDER intact, the same property
// TestMachineConfigRolesRoundTrip pins for [roles.tiers]. Order is load-bearing:
// ResolveModel walks a tier's candidates in declaration order and takes the
// first that resolves, so a reordering silently changes which model runs.
func TestMachineConfigPresetsRoundTrip(t *testing.T) {
	mc := &MachineConfig{
		MachineID: "m-1",
		Roles: &MachineRoles{
			Preset: "frugal",
			Presets: map[string]*RolesPreset{
				"frugal": {Tiers: map[string][]RoleCandidate{
					"default": {
						{Harness: "pi", Model: "first-choice"},
						{Harness: "pi", Model: "second-choice"},
					},
					"raised": {{Harness: "codex", Model: "gpt-5-codex"}},
				}},
			},
		},
	}

	b, err := toml.Marshal(mc)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	// The KEY PATH on disk is asserted, not just the round trip, and that
	// addition came out of a mutation run: renaming the `presets` toml tag
	// escaped a pure round-trip assertion entirely, because marshal and
	// unmarshal then agree with each other about the new name and the value
	// survives. A round trip cannot see a symmetric rename.
	//
	// It matters because this table is written BY HAND. `[roles.presets.<name>.tiers]`
	// is the text an operator types into config.toml and the text
	// SaveMachineConfig's footer documents, so the key name is a contract with
	// people, not merely with the encoder.
	// Each expectation is anchored with a leading newline, and that is not
	// cosmetic. An unanchored "preset = 'frugal'" is a SUBSTRING of
	// "active_preset = 'frugal'", so renaming the toml tag escaped this very
	// assertion on a mutation run: the check passed while the key an operator
	// has to type had changed underneath it.
	for _, want := range []string{"\n[roles.presets.frugal.tiers]", "\npreset = 'frugal'"} {
		if !contains(string(b), want) {
			t.Errorf("Marshal() did not produce %q; a hand-written config.toml using the\n"+
				"documented spelling would not load. Got:\n%s", want, b)
		}
	}

	var round MachineConfig
	if err := toml.Unmarshal(b, &round); err != nil {
		t.Fatalf("Unmarshal() error: %v\ntoml:\n%s", err, b)
	}
	if round.Roles == nil || round.Roles.Presets["frugal"] == nil {
		t.Fatalf("round-tripped preset is missing; marshaled toml:\n%s", b)
	}
	if round.Roles.Preset != "frugal" {
		t.Errorf("round-tripped [roles] preset = %q, want %q", round.Roles.Preset, "frugal")
	}
	got := round.Roles.Presets["frugal"].Tiers
	want := mc.Roles.Presets["frugal"].Tiers
	if !reflect.DeepEqual(got["default"], want["default"]) {
		t.Errorf("round-tripped preset Tiers[default] = %+v, want %+v (order must survive)",
			got["default"], want["default"])
	}
	if !reflect.DeepEqual(got["raised"], want["raised"]) {
		t.Errorf("round-tripped preset Tiers[raised] = %+v, want %+v", got["raised"], want["raised"])
	}
}

// TestMachineConfigRolesStillOmittedWithPresetFields re-pins the aihub#642
// "costs a machine that never touches it" contract AFTER aihub#673 added Preset
// and Presets to MachineRoles.
//
// TestMachineConfigRolesOmittedWhenUnset above already asserts this, and that is
// exactly why this one exists separately: that test was written before the new
// fields and would keep passing if a future edit made MachineRoles non-optional
// in some other way. This states the post-change obligation in its own words --
// adding fields to MachineRoles must never make an unconfigured machine start
// emitting a [roles] table, because config.toml is written back by
// SaveMachineConfig and junk written there is junk every later read inherits.
func TestMachineConfigRolesStillOmittedWithPresetFields(t *testing.T) {
	b, err := toml.Marshal(&MachineConfig{MachineID: "m-1"})
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	for _, forbidden := range []string{"[roles]", "[roles.tiers", "[roles.presets", "preset ="} {
		if contains(string(b), forbidden) {
			t.Errorf("Marshal() of an untouched machine emitted %q:\n%s", forbidden, b)
		}
	}
	var round MachineConfig
	if err := toml.Unmarshal(b, &round); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if round.Roles != nil {
		t.Errorf("round-tripped Roles = %+v, want nil", round.Roles)
	}
}

// TestSaveMachineConfigDocumentsPresets pins that a fresh config.toml tells its
// reader presets exist, in the same spirit as TestSaveMachineConfigRolesExample:
// a feature nobody can discover from the file they are editing is a feature
// nobody uses.
func TestSaveMachineConfigDocumentsPresets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveMachineConfig(&MachineConfig{MachineID: "m-1"}); err != nil {
		t.Fatalf("SaveMachineConfig() error: %v", err)
	}
	b, err := os.ReadFile(MachineConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if !contains(string(b), "roles.presets") {
		t.Errorf("SaveMachineConfig() footer does not mention roles.presets; got:\n%s", b)
	}
}

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

// TestValidateCandidates pins aihub#676 finding 6: a harness string had no
// validation anywhere in the repo, so `harness = "claude"` or a trailing space
// in `"opencode "` matched nothing forever and surfaced only as the generic
// "no resolvable <harness> model candidate" warning -- which sends the operator
// to check the MODEL, the one part of the line that was fine.
//
// It also pins the bare-model-id check, which is the config-side half of
// finding 1: aihub#676 measured pi 0.85.1 refusing an ambiguous bare id
// outright and resolving a seemingly-unique one to a different provider.
func TestValidateCandidates(t *testing.T) {
	tests := []struct {
		name string
		in   map[string][]RoleCandidate
		// wantEach: every string must appear in SOME problem.
		wantEach []string
		// wantNone: no problem may contain any of these.
		wantNone []string
		wantLen  int
	}{
		{
			name: "a fully correct table has nothing to say",
			in: map[string][]RoleCandidate{
				"default": {{Harness: "pi", Model: "sub2api-anthropic/claude-sonnet-4-5"}},
				"raised":  {{Harness: "codex", Model: "gpt-6-astra"}},
				"low":     {{Harness: "opencode", Model: "anthropic/claude-haiku-4-5"}},
			},
			wantLen: 0,
		},
		{
			name: "unknown harness is named, with the known set",
			in:   map[string][]RoleCandidate{"default": {{Harness: "claude", Model: "x/y"}}},
			// "cc, codex, opencode, pi" is asserted with the LEADING "cc, ", not
			// as the suffix "codex, opencode, pi" this used to check. That
			// suffix still matched after aihub#681 prepended "cc" to
			// ConfigurableHarnesses, i.e. the assertion would have stayed green
			// whether or not the new harness was advertised to the operator at
			// all -- a passing test that had stopped testing the thing.
			wantEach: []string{`unknown harness "claude"`, "cc, codex, opencode, pi", `tier "default" candidate 0`},
			wantLen:  1,
		},
		{
			// aihub#681 INVERTED this case. It used to be named "cc gets the
			// specific reason it is not configurable here" and pinned a hint
			// pointing at cc_aliases.yaml; cc is now a configurable harness, so
			// a cc candidate must be accepted in silence like any other. The
			// old expectation is kept as prose rather than as an assertion
			// precisely because the assertion is what changed sides.
			name:    "a cc candidate is valid and produces no problem at all",
			in:      map[string][]RoleCandidate{"raised": {{Harness: "cc", Model: "opus"}}},
			wantLen: 0,
		},
		{
			// The discriminator for the case above: cc is accepted because it is
			// KNOWN, not because ValidateCandidates stopped looking at the
			// harness field. A near-miss spelling must still be refused, and the
			// hint must now name the correct spelling rather than deny the
			// feature.
			name:     "a near-miss spelling of cc is still refused, and told the right spelling",
			in:       map[string][]RoleCandidate{"raised": {{Harness: "claude-code", Model: "opus"}}},
			wantEach: []string{`unknown harness "claude-code"`, `spelled "cc"`, "configurable"},
			wantNone: []string{"not configurable"},
			wantLen:  1,
		},
		{
			// cc takes a BARE alias, so the pi/opencode provider-prefix rule
			// must not reach it. Without this, "sonnet" would be one edit away
			// from being reported as a bare id needing "<provider>/sonnet".
			name:     "a bare cc alias is NOT flagged for lacking a provider prefix",
			in:       map[string][]RoleCandidate{"lowest": {{Harness: "cc", Model: "haiku"}}},
			wantNone: []string{"BARE"},
			wantLen:  0,
		},
		{
			name: "whitespace in a harness name is called out explicitly",
			in:   map[string][]RoleCandidate{"low": {{Harness: "opencode ", Model: "a/b"}}},
			// The discriminator: a bare "unknown harness" message would leave
			// the operator staring at a string that LOOKS right.
			wantEach: []string{"whitespace"},
			wantLen:  1,
		},
		{
			name:     "empty harness",
			in:       map[string][]RoleCandidate{"lowest": {{Harness: "", Model: "a/b"}}},
			wantEach: []string{"declares no harness"},
			wantLen:  1,
		},
		{
			name:     "empty model",
			in:       map[string][]RoleCandidate{"lowest": {{Harness: "pi", Model: ""}}},
			wantEach: []string{"declares no model"},
			// Must NOT also complain about the missing provider prefix: an
			// empty model has one problem, not two.
			wantNone: []string{"BARE"},
			wantLen:  1,
		},
		{
			name:     "bare pi model id is flagged with the form to write instead",
			in:       map[string][]RoleCandidate{"default": {{Harness: "pi", Model: "claude-sonnet-4-5"}}},
			wantEach: []string{"BARE pi model id", `"<provider>/claude-sonnet-4-5"`, "aihub#676"},
			wantLen:  1,
		},
		{
			name:     "bare opencode model id is flagged too",
			in:       map[string][]RoleCandidate{"low": {{Harness: "opencode", Model: "claude-haiku-4-5"}}},
			wantEach: []string{"BARE opencode model id"},
			wantLen:  1,
		},
		{
			name: "a codex slug is NOT flagged for lacking a provider prefix",
			// The negative control for the check above: codex slugs are bare by
			// design, so a rule that flagged every prefix-less model would be
			// noise on the one harness where bare is correct.
			in:       map[string][]RoleCandidate{"raised": {{Harness: "codex", Model: "gpt-6-astra"}}},
			wantNone: []string{"BARE"},
			wantLen:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateCandidates(tc.in)
			if len(got) != tc.wantLen {
				t.Fatalf("ValidateCandidates() returned %d problems, want %d: %v", len(got), tc.wantLen, got)
			}
			joined := ""
			for _, p := range got {
				joined += p + "\n"
			}
			for _, want := range tc.wantEach {
				if !contains(joined, want) {
					t.Errorf("no problem mentions %q; got:\n%s", want, joined)
				}
			}
			for _, never := range tc.wantNone {
				if contains(joined, never) {
					t.Errorf("a problem mentions %q, which should not apply here; got:\n%s", never, joined)
				}
			}
		})
	}
}

// TestValidateCandidatesIsDeterministic pins the stable ordering the callers
// print in: two runs over the same table must produce the same sequence, or a
// machine's warnings reshuffle between invocations for no reason.
func TestValidateCandidatesIsDeterministic(t *testing.T) {
	in := map[string][]RoleCandidate{
		"raised":  {{Harness: "nope", Model: "a"}},
		"default": {{Harness: "nope", Model: "b"}},
		"low":     {{Harness: "nope", Model: "c"}},
		"lowest":  {{Harness: "nope", Model: "d"}},
	}
	first := ValidateCandidates(in)
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(ValidateCandidates(in), first) {
			t.Fatalf("ValidateCandidates() is not order-stable across runs: %v vs %v", ValidateCandidates(in), first)
		}
	}
	// And the order is the documented one (tier name, ascending), not map order.
	want := []string{"default", "low", "lowest", "raised"}
	for i, tier := range want {
		if !contains(first[i], `tier "`+tier+`"`) {
			t.Errorf("problem %d is %q, want it to concern tier %q", i, first[i], tier)
		}
	}
}

// TestUnreadRolesOverrideDir pins aihub#676 finding 2. aihub#642's design
// promised a `~/.polyforge/roles/` user-override layer that was never
// implemented; aihub#676 withdrew it (see UnreadRolesOverrideDir's doc comment
// for the structural reason) and replaced the silent no-op with a notice.
//
// The assertion that matters is the middle one: an EMPTY directory must not
// warn. A check that fired on mere existence would nag every machine that ever
// ran `mkdir` there, and a warning nobody can silence by doing the right thing
// gets ignored.
func TestUnreadRolesOverrideDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, present := UnreadRolesOverrideDir()
	if present {
		t.Errorf("UnreadRolesOverrideDir() = (%q, true) with no such directory", path)
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if _, present = UnreadRolesOverrideDir(); present {
		t.Error("UnreadRolesOverrideDir() reports present for an EMPTY directory; nothing is being ignored yet")
	}

	if err := os.WriteFile(path+"/reviewer.yaml", []byte("name: reviewer\n"), 0o644); err != nil {
		t.Fatalf("write override file: %v", err)
	}
	got, present := UnreadRolesOverrideDir()
	if !present {
		t.Fatalf("UnreadRolesOverrideDir() = (%q, false) with a file in it; the operator's edits would "+
			"go unmentioned, which is the aihub#676 defect", got)
	}

	// The notice has to say the thing the operator needs: that it is ignored,
	// and where role definitions really come from.
	msg := RolesOverrideIgnoredWarning(got)
	for _, want := range []string{got, "NOT read", "internal/roles/definitions"} {
		if !contains(msg, want) {
			t.Errorf("RolesOverrideIgnoredWarning() does not mention %q; got:\n%s", want, msg)
		}
	}
}
