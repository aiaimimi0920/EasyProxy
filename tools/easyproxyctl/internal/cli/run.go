package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/gitstate"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/manifest"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/naming"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/topology"
)

const version = "0.1.0"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "version":
		_, err = fmt.Fprintln(stdout, version)
	case "topology":
		err = runTopology(args[1:], stdout, stderr)
	case "manifest":
		err = runManifest(args[1:], stdout, stderr)
	case "cloud", "local":
		err = fmt.Errorf("%s lifecycle commands are not implemented until their delivery phase", args[0])
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func runTopology(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("topology subcommand is required: validate, show, or names")
	}
	if args[0] != "validate" && args[0] != "show" && args[0] != "names" {
		return fmt.Errorf("unknown topology subcommand %q", args[0])
	}
	flags := flag.NewFlagSet("topology "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("file", "topology.yaml", "topology YAML path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	loaded, err := topology.Load(*path)
	if err != nil {
		return err
	}
	switch args[0] {
	case "validate":
		_, err = fmt.Fprintf(stdout, "valid topology: %s\n", loaded.DeploymentName)
		return err
	case "names":
		return writeJSON(stdout, naming.Resolve(loaded))
	case "show":
		return writeJSON(stdout, loaded)
	}
	return nil
}

func runManifest(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("manifest subcommand is required: build or verify")
	}
	switch args[0] {
	case "build":
		return buildManifest(args[1:], stdout, stderr)
	case "verify":
		flags := flag.NewFlagSet("manifest verify", flag.ContinueOnError)
		flags.SetOutput(stderr)
		path := flags.String("file", "deployment-manifest.json", "deployment manifest path")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		value, err := manifest.Read(*path)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "valid manifest: %s\n", value.DeploymentName)
		return err
	default:
		return fmt.Errorf("unknown manifest subcommand %q", args[0])
	}
}

func buildManifest(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("manifest build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	topologyPath := flags.String("topology", "topology.yaml", "topology YAML path")
	outputPath := flags.String("output", "deployment-manifest.json", "output path")
	repoRoot := flags.String("repo-root", ".", "Git repository root used for source provenance")
	rootCommit := flags.String("root-commit", "", "root Git commit override")
	workflowRun := flags.String("workflow-run", os.Getenv("GITHUB_RUN_ID"), "workflow run identifier")
	submoduleCommits := assignmentFlag{}
	images := assignmentFlag{}
	resourceIDs := assignmentFlag{}
	flags.Var(&submoduleCommits, "submodule-commit", "submodule path=full commit override; repeatable")
	flags.Var(&images, "image", "image name=immutable reference; repeatable")
	flags.Var(&resourceIDs, "resource-id", "resource kind=provider ID; repeatable")
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
	canonical, err := loaded.CanonicalSHA256Input()
	if err != nil {
		return err
	}
	topologyDigest := sha256.Sum256(canonical)
	names := naming.Resolve(loaded)
	components := enabledComponents(loaded)
	resources, err := enabledResources(loaded, names, resourceIDs)
	if err != nil {
		return err
	}
	rootSourceCommit, sourceCommits, err := collectSourceState(*repoRoot, *rootCommit, submoduleCommits)
	if err != nil {
		return err
	}
	if err := requireEnabledSubmodules(loaded, sourceCommits); err != nil {
		return err
	}
	value := &manifest.Manifest{
		SchemaVersion:  manifest.SchemaVersion,
		DeploymentName: loaded.DeploymentName,
		ReleaseChannel: loaded.ReleaseChannel,
		GeneratedAt:    time.Now().UTC(),
		WorkflowRun:    *workflowRun,
		TopologySHA256: hex.EncodeToString(topologyDigest[:]),
		Components:     components,
		Resources:      resources,
		Source: manifest.Source{
			RootCommit:       rootSourceCommit,
			SubmoduleCommits: sourceCommits,
		},
		Images: map[string]string(images),
	}
	if err := manifest.Write(*outputPath, value); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote manifest: %s\n", *outputPath)
	return err
}

type assignmentFlag map[string]string

func (values *assignmentFlag) String() string {
	if values == nil {
		return ""
	}
	return fmt.Sprint(map[string]string(*values))
}

func (values *assignmentFlag) Set(value string) error {
	key, item, ok := strings.Cut(value, "=")
	key = strings.TrimSpace(key)
	item = strings.TrimSpace(item)
	if !ok || key == "" || item == "" {
		return errors.New("assignment must use non-empty key=value")
	}
	if _, exists := (*values)[key]; exists {
		return fmt.Errorf("duplicate assignment for %q", key)
	}
	(*values)[key] = item
	return nil
}

func enabledComponents(loaded *topology.Topology) []string {
	components := make([]string, 0, 4)
	if loaded.Components.AggregatorEnabled() {
		components = append(components, "aggregator")
	}
	if loaded.Components.MiSubEnabled() {
		components = append(components, "misub")
	}
	if loaded.Components.ECHWorkerEnabled() {
		components = append(components, "ech_worker")
	}
	if loaded.Components.LocalEasyProxyEnabled() {
		components = append(components, "local_easyproxy")
	}
	return components
}

func enabledResources(loaded *topology.Topology, names naming.ResourceNames, ids map[string]string) ([]manifest.Resource, error) {
	resources := make([]manifest.Resource, 0, 4)
	usedKinds := make(map[string]struct{})
	add := func(kind, name, url string) {
		resources = append(resources, manifest.Resource{Kind: kind, Name: name, ID: ids[kind], URL: url})
		usedKinds[kind] = struct{}{}
	}
	if loaded.Components.MiSubEnabled() {
		add("pages", names.PagesProject, "")
		add("d1", names.D1Database, "")
	}
	if loaded.Components.ECHWorkerEnabled() {
		add("worker", names.ECHWorker, "")
	}
	if loaded.Components.AggregatorEnabled() {
		add("r2", names.R2Bucket, loaded.Cloudflare.Resources.R2PublicBaseURL)
	}
	for kind := range ids {
		if _, ok := usedKinds[kind]; !ok {
			return nil, fmt.Errorf("resource ID supplied for disabled or unknown kind %q", kind)
		}
	}
	return resources, nil
}

func collectSourceState(repoRoot, rootOverride string, overrides map[string]string) (string, map[string]string, error) {
	commits := make(map[string]string)
	state, collectErr := gitstate.Collect(repoRoot)
	if collectErr == nil {
		for path, commit := range state.Submodules {
			commits[path] = commit
		}
		if rootOverride == "" {
			rootOverride = state.RootCommit
		}
	}
	for path, commit := range overrides {
		commits[path] = commit
	}
	if rootOverride == "" {
		return "", nil, fmt.Errorf("collect Git source provenance: %w; provide --root-commit and --submodule-commit overrides", collectErr)
	}
	return rootOverride, commits, nil
}

func requireEnabledSubmodules(loaded *topology.Topology, commits map[string]string) error {
	required := make([]string, 0, 3)
	if loaded.Components.AggregatorEnabled() {
		required = append(required, "upstreams/aggregator")
	}
	if loaded.Components.MiSubEnabled() {
		required = append(required, "upstreams/misub")
	}
	if loaded.Components.ECHWorkerEnabled() {
		required = append(required, "upstreams/ech-workers")
	}
	for _, path := range required {
		if commits[path] == "" {
			return fmt.Errorf("enabled component source commit is missing for %s", path)
		}
	}
	return nil
}

func writeJSON(writer io.Writer, value interface{}) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: easyproxyctl <topology|manifest|cloud|local|version> [subcommand] [flags]")
}
