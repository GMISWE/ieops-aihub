package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// RunSkills dispatches the explicit authenticated registry bootstrap/import
// operations. Neither command changes visibility or grants: every publication
// therefore keeps the domain's private default.
func RunSkills(ctx context.Context, c *client.Client, args []string) {
	out, err := runSkills(ctx, c, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills: %v\n", err)
		os.Exit(1)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills: marshal result: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
}

func runSkills(ctx context.Context, store skillregistry.SeedStore, args []string) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: polyforge skills <seed|import> [flags]")
	}
	switch args[0] {
	case "seed":
		if len(args) != 1 {
			return nil, fmt.Errorf("seed takes no flags; it publishes the canonical private bundle set")
		}
		return skillregistry.ApplySeed(ctx, store)
	case "import":
		return runSkillImport(ctx, store, args[1:])
	default:
		return nil, fmt.Errorf("unknown verb %q (want seed or import)", args[0])
	}
}

func runSkillImport(ctx context.Context, store skillregistry.SeedStore, args []string) (skillregistry.SeedApplyResult, error) {
	allowed := map[string]bool{
		"name": true, "root": true, "entry": true, "contract-file": true,
		"license-name": true, "license-url": true, "license-notice-file": true,
		"provenance-source": true, "upstream-url": true, "upstream-commit": true,
		"upstream-license": true, "provenance-notes": true,
	}
	values := map[string]string{}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") || !strings.Contains(arg, "=") {
			return skillregistry.SeedApplyResult{}, fmt.Errorf("import flags use --name=value form; got %q", arg)
		}
		key, value, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !allowed[key] {
			return skillregistry.SeedApplyResult{}, fmt.Errorf("unknown import flag --%s", key)
		}
		if _, duplicate := values[key]; duplicate {
			return skillregistry.SeedApplyResult{}, fmt.Errorf("import flag --%s was supplied twice", key)
		}
		values[key] = value
	}
	for _, required := range []string{"name", "root", "entry", "contract-file", "license-name"} {
		if values[required] == "" {
			return skillregistry.SeedApplyResult{}, fmt.Errorf("import requires --%s", required)
		}
	}
	contractRaw, err := os.ReadFile(values["contract-file"])
	if err != nil {
		return skillregistry.SeedApplyResult{}, fmt.Errorf("read --contract-file: %w", err)
	}
	contract, err := skillregistry.DecodeContract(contractRaw)
	if err != nil {
		return skillregistry.SeedApplyResult{}, fmt.Errorf("--contract-file: %w", err)
	}
	notice := ""
	if path := values["license-notice-file"]; path != "" {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return skillregistry.SeedApplyResult{}, fmt.Errorf("read --license-notice-file: %w", readErr)
		}
		notice = string(data)
	}
	return skillregistry.ApplyImport(ctx, store, values["name"], values["root"], skillregistry.ImportOptions{
		Entry: values["entry"],
		License: skillregistry.BundleLicense{
			Name: values["license-name"], URL: values["license-url"], Notice: notice,
		},
		Provenance: skillregistry.BundleProvenance{
			Source: values["provenance-source"], UpstreamURL: values["upstream-url"],
			UpstreamCommit: values["upstream-commit"], UpstreamLicense: values["upstream-license"],
			Notes: values["provenance-notes"],
		},
	}, contract)
}
