package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/cloudflare"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/discovery"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/manifest"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/topology"
)

func runCloud(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("cloud subcommand is required: bootstrap, update, verify, backup, or restore")
	}
	switch args[0] {
	case "bootstrap", "update", "verify":
		return runCloudResources(args[0], args[1:], stdout, stderr)
	case "backup":
		return runCloudBackup(args[1:], stdout, stderr)
	case "restore":
		return runCloudRestore(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown cloud subcommand %q", args[0])
	}
}

func runCloudResources(command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cloud "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	topologyPath := flags.String("topology", "topology.yaml", "topology YAML path")
	manifestPath := flags.String("manifest", "deployment-manifest.json", "deployment manifest path")
	stateOutput := flags.String("state-output", "cloud-resources.json", "resolved non-secret resource state path")
	wranglerConfig := flags.String("wrangler-config", "", "materialized Wrangler JSON config to verify")
	wranglerCommand := flags.String("wrangler-command", "npx", "Wrangler launcher executable")
	wranglerPrefix := flags.String("wrangler-prefix", "wrangler", "arguments placed before Wrangler arguments")
	wranglerDir := flags.String("wrangler-dir", "upstreams/misub", "Wrangler working directory")
	productionBranch := flags.String("production-branch", "main", "Pages production branch used on bootstrap")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	loaded, err := topology.Load(*topologyPath)
	if err != nil {
		return err
	}
	mode := discovery.ModeUpdate
	if command == "bootstrap" {
		mode = discovery.ModeBootstrap
	}
	provider := cloudflare.Provider{
		Runner: cloudflare.CommandRunner{
			Executable: *wranglerCommand,
			Prefix:     strings.Fields(*wranglerPrefix),
			Dir:        *wranglerDir,
		},
		ProductionBranch: *productionBranch,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	state, err := cloudflare.ResolveMiSub(ctx, provider, mode, loaded)
	if err != nil {
		return err
	}
	if command == "verify" {
		value, err := manifest.Read(*manifestPath)
		if err != nil {
			return err
		}
		if err := cloudflare.VerifyManifest(loaded, state, value); err != nil {
			return err
		}
	}
	if command == "verify" && *wranglerConfig == "" {
		return errors.New("cloud verify requires --wrangler-config")
	}
	if *wranglerConfig != "" {
		if err := cloudflare.VerifyWranglerConfig(*wranglerConfig, state); err != nil {
			return err
		}
	}
	if err := cloudflare.WriteState(*stateOutput, state); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s cloud resources: pages=%s d1=%s\n", command, state.Pages.ID, state.D1.ID)
	return err
}
