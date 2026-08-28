package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/backup"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/cloudflare"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/discovery"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/manifest"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/misub"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/topology"
)

type protectedCloud struct {
	Topology *topology.Topology
	Provider cloudflare.Provider
	D1       cloudflare.D1
	State    cloudflare.State
}

type cloudProtectionFlags struct {
	topologyPath    string
	manifestPath    string
	wranglerConfig  string
	wranglerCommand string
	wranglerPrefix  string
	wranglerDir     string
}

func addCloudProtectionFlags(flags *flag.FlagSet) *cloudProtectionFlags {
	value := &cloudProtectionFlags{}
	flags.StringVar(&value.topologyPath, "topology", "topology.yaml", "topology YAML path")
	flags.StringVar(&value.manifestPath, "manifest", "deployment-manifest.json", "deployment manifest path")
	flags.StringVar(&value.wranglerConfig, "wrangler-config", "", "materialized Wrangler JSON config")
	flags.StringVar(&value.wranglerCommand, "wrangler-command", "npx", "Wrangler launcher executable")
	flags.StringVar(&value.wranglerPrefix, "wrangler-prefix", "wrangler", "arguments placed before Wrangler arguments")
	flags.StringVar(&value.wranglerDir, "wrangler-dir", "upstreams/misub", "Wrangler working directory")
	return value
}

func (value cloudProtectionFlags) resolve(ctx context.Context) (protectedCloud, error) {
	if value.wranglerConfig == "" {
		return protectedCloud{}, errors.New("--wrangler-config is required")
	}
	loaded, err := topology.Load(value.topologyPath)
	if err != nil {
		return protectedCloud{}, err
	}
	runner := cloudflare.CommandRunner{Executable: value.wranglerCommand, Prefix: strings.Fields(value.wranglerPrefix), Dir: value.wranglerDir}
	provider := cloudflare.Provider{Runner: runner}
	state, err := cloudflare.ResolveMiSub(ctx, provider, discovery.ModeUpdate, loaded)
	if err != nil {
		return protectedCloud{}, err
	}
	valueManifest, err := manifest.Read(value.manifestPath)
	if err != nil {
		return protectedCloud{}, err
	}
	if err := cloudflare.VerifyManifest(loaded, state, valueManifest); err != nil {
		return protectedCloud{}, err
	}
	if err := cloudflare.VerifyWranglerConfig(value.wranglerConfig, state); err != nil {
		return protectedCloud{}, err
	}
	return protectedCloud{Topology: loaded, Provider: provider, D1: cloudflare.D1{Runner: runner}, State: state}, nil
}

func runCloudBackup(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cloud backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := addCloudProtectionFlags(flags)
	baseURL := flags.String("base-url", "", "optional deployed MiSub URL used for authenticated logical export")
	output := flags.String("output", "misub-backup.age", "encrypted backup output path")
	allowDirect := flags.Bool("allow-direct-d1-fallback", false, "allow an explicit direct-D1 logical export fallback")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cloud, err := common.resolve(ctx)
	if err != nil {
		return err
	}
	if err := createCloudBackup(ctx, cloud, common.manifestPath, *baseURL, *output, *allowDirect); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "encrypted MiSub backup written: %s\n", *output)
	return err
}

func createCloudBackup(ctx context.Context, cloud protectedCloud, manifestPath, baseURL, output string, allowDirect bool) error {
	passphrase, err := requiredSecret(cloud.Topology.Secrets.MiSubBackupSecret)
	if err != nil {
		return err
	}
	temporaryDir, err := os.MkdirTemp("", "easyproxy-misub-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporaryDir)
	databasePath := filepath.Join(temporaryDir, backup.DatabaseFile)
	logicalPath := filepath.Join(temporaryDir, backup.LogicalFile)

	var logical misub.Export
	var snapshot cloudflare.D1Snapshot
	stable := false
	for attempt := 1; attempt <= 3; attempt++ {
		_ = os.Remove(databasePath)
		before, err := cloud.D1.Snapshot(ctx, cloud.State.D1.Name)
		if err != nil {
			return err
		}
		if err := cloud.D1.Export(ctx, cloud.State.D1.Name, databasePath); err != nil {
			return err
		}
		logical, err = exportLogicalBackup(ctx, cloud, baseURL, before, allowDirect)
		if err != nil {
			return err
		}
		after, err := cloud.D1.Snapshot(ctx, cloud.State.D1.Name)
		if err != nil {
			return err
		}
		if cloudflare.EqualSnapshot(before, after) {
			snapshot = after
			stable = true
			break
		}
	}
	if !stable {
		return errors.New("D1 changed during three backup attempts; no backup was published")
	}
	if err := verifyLogicalIdentity(logical, cloud.State); err != nil {
		return err
	}
	if err := os.WriteFile(logicalPath, append(logical.Raw, '\n'), 0o600); err != nil {
		return err
	}
	metadata := backup.Metadata{
		DeploymentName: cloud.Topology.DeploymentName, ApplicationVersion: logical.ApplicationVersion,
		DatabaseSchemaVersion: snapshot.SchemaVersion, DatabaseName: cloud.State.D1.Name,
		DatabaseID: cloud.State.D1.ID, DatabaseBinding: cloud.State.D1Binding,
		Counts: logical.Counts, LogicalDataSHA256: logical.LogicalDataSHA256,
		DatabaseRowsSHA256: snapshot.SHA256, TableRows: snapshot.Rows,
	}
	return backup.CreateEncrypted(output, passphrase, backup.Input{
		DatabasePath: databasePath, LogicalPath: logicalPath, ManifestPath: manifestPath, Metadata: metadata,
	})
}

func exportLogicalBackup(ctx context.Context, cloud protectedCloud, baseURL string, snapshot cloudflare.D1Snapshot, allowDirect bool) (misub.Export, error) {
	adminPassword := os.Getenv(cloud.Topology.Secrets.MiSubAdminPassword)
	if strings.TrimSpace(baseURL) != "" && adminPassword != "" {
		client, err := misub.NewClient(baseURL, 90*time.Second)
		if err == nil {
			err = client.Login(ctx, adminPassword)
		}
		if err == nil {
			var result misub.Export
			result, err = client.Export(ctx)
			if err == nil {
				err = verifyLogicalIdentity(result, cloud.State)
			}
			if err == nil {
				return result, nil
			}
		}
		if !allowDirect {
			return misub.Export{}, fmt.Errorf("authenticated MiSub logical export failed: %w", err)
		}
	} else if !allowDirect {
		return misub.Export{}, errors.New("authenticated logical export is unavailable; direct D1 fallback was not authorized")
	}
	storage, err := cloud.D1.ReadMiSubStorage(ctx, cloud.State.D1.Name)
	if err != nil {
		return misub.Export{}, fmt.Errorf("build direct D1 logical backup: %w", err)
	}
	if err := validateStoragePresence(storage, snapshot); err != nil {
		return misub.Export{}, err
	}
	return misub.BuildExportFromStorage(misub.StorageData{
		Sources: storage.Sources, Profiles: storage.Profiles, Settings: storage.Settings, Cron: storage.Cron,
	}, snapshot.SchemaVersion, misub.ResourceIdentity{
		DeploymentName: cloud.State.DeploymentName, PagesProject: cloud.State.Pages.Name,
		D1DatabaseID: cloud.State.D1.ID, D1Binding: cloud.State.D1Binding,
	})
}

func validateStoragePresence(storage cloudflare.MiSubStorage, snapshot cloudflare.D1Snapshot) error {
	presence := map[string]bool{
		"subscriptions": storage.SourcesPresent, "profiles": storage.ProfilesPresent,
		"settings": storage.SettingsPresent, "cron_executions": storage.CronPresent,
	}
	for table, rows := range snapshot.Rows {
		if rows > 0 && !presence[table] && table != "schema_migrations" {
			return fmt.Errorf("direct D1 logical backup cannot find canonical row in non-empty %s table", table)
		}
	}
	return nil
}

func verifyLogicalIdentity(value misub.Export, state cloudflare.State) error {
	checks := []struct{ actual, expected, field string }{
		{value.DeploymentName, state.DeploymentName, "deployment name"},
		{value.PagesProject, state.Pages.Name, "Pages project"},
		{value.D1DatabaseID, state.D1.ID, "D1 database ID"},
		{value.D1Binding, state.D1Binding, "D1 binding"},
	}
	for _, check := range checks {
		if check.actual == "" || check.actual != check.expected {
			return fmt.Errorf("logical backup %s does not match protected resource", check.field)
		}
	}
	return nil
}

func requiredSecret(envName string) (string, error) {
	value, ok := os.LookupEnv(envName)
	if !ok || value == "" {
		return "", fmt.Errorf("required secret environment variable %s is empty", envName)
	}
	return value, nil
}

func runCloudRestore(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cloud restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := addCloudProtectionFlags(flags)
	input := flags.String("input", "", "encrypted backup path")
	targetName := flags.String("target-database-name", "", "existing restore target D1 name")
	targetID := flags.String("target-database-id", "", "existing restore target D1 ID")
	confirmID := flags.String("confirm-database-id", "", "exact target D1 ID confirmation")
	drill := flags.Bool("drill", false, "require an isolated restore-drill target")
	allowProduction := flags.Bool("allow-production-restore", false, "allow restore into the protected production D1")
	baseURL := flags.String("base-url", "", "MiSub URL used for automatic pre-restore backup")
	preRestoreOutput := flags.String("pre-restore-backup", "", "encrypted automatic pre-restore backup path")
	allowDirect := flags.Bool("allow-direct-d1-fallback", false, "allow direct D1 fallback for the automatic pre-restore backup")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *targetName == "" || *targetID == "" {
		return errors.New("--input, --target-database-name, and --target-database-id are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cloud, err := common.resolve(ctx)
	if err != nil {
		return err
	}
	if err := validateRestoreTarget(ctx, cloud, *targetName, *targetID, *confirmID, *drill, *allowProduction); err != nil {
		return err
	}
	if !*drill {
		if *preRestoreOutput == "" {
			return errors.New("production restore requires --pre-restore-backup")
		}
		if err := createCloudBackup(ctx, cloud, common.manifestPath, *baseURL, *preRestoreOutput, *allowDirect); err != nil {
			return fmt.Errorf("automatic pre-restore backup failed: %w", err)
		}
	}
	passphrase, err := requiredSecret(cloud.Topology.Secrets.MiSubBackupSecret)
	if err != nil {
		return err
	}
	temporaryDir, err := os.MkdirTemp("", "easyproxy-misub-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporaryDir)
	extracted, err := backup.ExtractEncrypted(*input, temporaryDir, passphrase)
	if err != nil {
		return err
	}
	if err := verifyExtractedBackup(cloud, extracted); err != nil {
		return err
	}
	if err := cloud.D1.Restore(ctx, *targetName, extracted.Database); err != nil {
		return err
	}
	snapshot, err := cloud.D1.Snapshot(ctx, *targetName)
	if err != nil {
		return err
	}
	expected := cloudflare.D1Snapshot{SHA256: extracted.Metadata.DatabaseRowsSHA256, SchemaVersion: extracted.Metadata.DatabaseSchemaVersion, Rows: extracted.Metadata.TableRows}
	if !cloudflare.EqualSnapshot(expected, snapshot) {
		return errors.New("restored D1 content does not match backup snapshot")
	}
	_, err = fmt.Fprintf(stdout, "verified MiSub restore: database=%s id=%s\n", *targetName, *targetID)
	return err
}

func validateRestoreTarget(ctx context.Context, cloud protectedCloud, name, id, confirmation string, drill, allowProduction bool) error {
	if confirmation == "" || confirmation != id {
		return errors.New("--confirm-database-id must exactly match --target-database-id")
	}
	matches, err := cloud.Provider.FindExact(ctx, "d1", name)
	if err != nil {
		return err
	}
	if len(matches) != 1 || matches[0].ID != id {
		return errors.New("restore target does not resolve to exactly one matching D1 identity")
	}
	if drill {
		prefix := cloud.State.D1.Name + "-restore-drill-"
		if id == cloud.State.D1.ID || !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("drill target must be a non-production D1 named %s*", prefix)
		}
		return nil
	}
	if !allowProduction || name != cloud.State.D1.Name || id != cloud.State.D1.ID {
		return errors.New("production restore requires --allow-production-restore and the exact protected D1 identity")
	}
	return nil
}

func verifyExtractedBackup(cloud protectedCloud, extracted backup.Extracted) error {
	metadata := extracted.Metadata
	if metadata.DeploymentName != cloud.Topology.DeploymentName || metadata.DatabaseName != cloud.State.D1.Name || metadata.DatabaseID != cloud.State.D1.ID || metadata.DatabaseBinding != cloud.State.D1Binding {
		return errors.New("backup source identity does not match the protected deployment")
	}
	archivedManifest, err := manifest.Read(extracted.Manifest)
	if err != nil {
		return err
	}
	if err := cloudflare.VerifyManifest(cloud.Topology, cloud.State, archivedManifest); err != nil {
		return err
	}
	logicalRaw, err := os.ReadFile(extracted.Logical)
	if err != nil {
		return err
	}
	logical, err := misub.ValidateExport(json.RawMessage(logicalRaw))
	if err != nil {
		return err
	}
	if logical.LogicalDataSHA256 != metadata.LogicalDataSHA256 {
		return errors.New("logical backup checksum does not match archive metadata")
	}
	return verifyLogicalIdentity(logical, cloud.State)
}
