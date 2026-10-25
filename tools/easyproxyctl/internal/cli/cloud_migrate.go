package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/backup"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/cloudflare"
)

type migrationWriter interface {
	Apply(context.Context, string, cloudflare.MigrationPlan) error
}

type migrationEvidence struct {
	SchemaVersion int    `json:"schema_version"`
	DatabaseID    string `json:"database_id"`
	BackupSHA256  string `json:"backup_sha256"`
	BeforeSHA256  string `json:"before_sha256"`
	AfterSHA256   string `json:"after_sha256"`
	BackupCreated bool   `json:"backup_created"`
	DataPreserved bool   `json:"data_preserved"`
	Verified      bool   `json:"migration_verified"`
}

func runCloudMigrate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("cloud migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := addCloudProtectionFlags(flags)
	directory := flags.String("migrations-dir", "", "directory containing only reviewed expand-only migrations")
	accountID := flags.String("account-id", "", "exact approved Cloudflare account ID")
	confirmation := flags.String("confirm-database-id", "", "exact approved D1 ID confirmation")
	baseURL := flags.String("base-url", "", "deployed MiSub URL for authenticated logical export")
	output := flags.String("backup-output", "", "new encrypted pre-migration backup path")
	input := flags.String("backup-input", "", "existing verified backup already retained by the suite caller")
	expectedSHA := flags.String("expected-backup-sha256", "", "exact SHA256 read back from durable backup storage")
	allowDirect := flags.Bool("allow-direct-d1-fallback", false, "explicitly authorize direct D1 logical export")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || (*output == "") == (*input == "") || *confirmation == "" || *accountID == "" ||
		*accountID != os.Getenv("CLOUDFLARE_ACCOUNT_ID") {
		return errors.New("EASYPROXY_MIGRATION_ARGUMENTS_REJECTED")
	}
	decoded, decodeErr := hex.DecodeString(*expectedSHA)
	if (*input != "" && (decodeErr != nil || len(decoded) != sha256.Size)) || (*output != "" && *expectedSHA != "") {
		return errors.New("EASYPROXY_MIGRATION_ARGUMENTS_REJECTED")
	}
	plan, err := cloudflare.LoadReviewedMigrations(*directory)
	if err != nil {
		return err
	}
	writer, err := cloudflare.NewD1SingleWriter(*accountID, os.Getenv("CLOUDFLARE_API_TOKEN"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	cloud, err := common.resolve(ctx)
	if err != nil || cloud.State.D1.ID != *confirmation || cloud.Topology.Cloudflare.AccountIDEnv != "CLOUDFLARE_ACCOUNT_ID" {
		return errors.New("EASYPROXY_MIGRATION_IDENTITY_REJECTED")
	}
	backupPath := *output
	if *input != "" {
		backupPath = *input
	}
	evidence, err := protectedMigrate(ctx, cloud, common.manifestPath, *baseURL, backupPath, *allowDirect, plan, writer, *expectedSHA)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(evidence)
}

// The suite caller must already own its persistent writer permit. This command
// neither acquires nor releases that permit, and never performs automatic restore.
func protectedMigrate(ctx context.Context, cloud protectedCloud, manifestPath, baseURL, output string, allowDirect bool,
	plan cloudflare.MigrationPlan, writer migrationWriter, expectedBackupSHA string) (migrationEvidence, error) {
	evidence := migrationEvidence{SchemaVersion: 1, DatabaseID: cloud.State.D1.ID}
	fail := func(code string) (migrationEvidence, error) { return evidence, errors.New(code) }
	before, err := cloud.D1.CapturePreservation(ctx, cloud.State.D1.Name)
	if err != nil || cloudflare.ValidateReviewedSchema(before, false) != nil {
		return fail("EASYPROXY_MIGRATION_PREFLIGHT_REJECTED")
	}
	var backedUp cloudflare.D1Snapshot
	if expectedBackupSHA != "" {
		backedUp, err = readMigrationBackup(cloud, output, expectedBackupSHA)
	} else {
		backedUp, err = createCloudBackupCaptured(ctx, cloud, manifestPath, baseURL, output, allowDirect)
	}
	if err != nil {
		return fail("EASYPROXY_MIGRATION_BACKUP_FAILED")
	}
	evidence.BackupCreated = true
	evidence.BackupSHA256 = expectedBackupSHA
	if expectedBackupSHA == "" {
		// Standalone mode retains a new local archive. SBA always supplies the
		// expected digest of an already-persisted backup instead.
		archive, openErr := os.Open(output)
		if openErr != nil {
			return fail("EASYPROXY_MIGRATION_BACKUP_FAILED")
		}
		digest := sha256.New()
		_, hashErr := io.Copy(digest, archive)
		closeErr := archive.Close()
		if hashErr != nil || closeErr != nil {
			return fail("EASYPROXY_MIGRATION_BACKUP_FAILED")
		}
		evidence.BackupSHA256 = hex.EncodeToString(digest.Sum(nil))
	}
	baseline, err := cloud.D1.CapturePreservation(ctx, cloud.State.D1.Name)
	if err != nil || baseline.Digest() != before.Digest() || backedUp.PreservationSHA256 != baseline.Digest() {
		return fail("EASYPROXY_MIGRATION_SOURCE_CHANGED")
	}
	current, err := cloud.D1.Snapshot(ctx, cloud.State.D1.Name)
	if err != nil || !cloudflare.EqualSnapshot(backedUp, current) {
		return fail("EASYPROXY_MIGRATION_SOURCE_CHANGED")
	}
	evidence.BeforeSHA256 = baseline.Digest()
	if err := writer.Apply(ctx, cloud.State.D1.ID, plan); err != nil {
		return fail("EASYPROXY_MIGRATION_WRITE_UNKNOWN")
	}
	after, err := cloud.D1.CapturePreservation(ctx, cloud.State.D1.Name)
	if err != nil {
		return fail("EASYPROXY_MIGRATION_VERIFY_UNKNOWN")
	}
	if err := cloudflare.VerifyExpansion(baseline, after); err != nil {
		return fail("EASYPROXY_MIGRATION_DATA_NOT_PRESERVED")
	}
	evidence.AfterSHA256 = after.Digest()
	evidence.DataPreserved, evidence.Verified = true, true
	return evidence, nil
}

func readMigrationBackup(cloud protectedCloud, path, expectedSHA string) (cloudflare.D1Snapshot, error) {
	directory, err := os.MkdirTemp("", "easyproxy-migration-backup-*")
	if err != nil {
		return cloudflare.D1Snapshot{}, err
	}
	defer os.RemoveAll(directory)
	// Copy once while hashing, then decrypt only that verified copy. Changes to
	// the caller's path after durable retention cannot switch the archive used.
	input, err := os.Open(path)
	if err != nil {
		return cloudflare.D1Snapshot{}, err
	}
	defer input.Close()
	copyPath := filepath.Join(directory, "retained.age")
	output, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return cloudflare.D1Snapshot{}, err
	}
	digest := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(output, digest), io.LimitReader(input, (512<<20)+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil || count > 512<<20 || hex.EncodeToString(digest.Sum(nil)) != expectedSHA {
		return cloudflare.D1Snapshot{}, errors.New("EASYPROXY_MIGRATION_RETAINED_BACKUP_MISMATCH")
	}
	passphrase, err := requiredSecret(cloud.Topology.Secrets.MiSubBackupSecret)
	if err != nil {
		return cloudflare.D1Snapshot{}, err
	}
	extractPath := filepath.Join(directory, "extracted")
	if err := os.Mkdir(extractPath, 0o700); err != nil {
		return cloudflare.D1Snapshot{}, err
	}
	extracted, err := backup.ExtractEncrypted(copyPath, extractPath, passphrase)
	if err != nil {
		return cloudflare.D1Snapshot{}, err
	}
	if err := verifyExtractedBackup(cloud, extracted); err != nil {
		return cloudflare.D1Snapshot{}, err
	}
	return cloudflare.D1Snapshot{SHA256: extracted.Metadata.DatabaseRowsSHA256,
		SchemaVersion: extracted.Metadata.DatabaseSchemaVersion, Rows: extracted.Metadata.TableRows,
		PreservationSHA256: extracted.Metadata.PreservationSHA256}, nil
}
