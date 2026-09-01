package config

import "testing"

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
