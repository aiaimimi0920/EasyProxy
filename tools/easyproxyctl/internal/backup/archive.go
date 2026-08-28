package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
)

const SchemaVersion = 1

const (
	DatabaseFile = "database.sql"
	LogicalFile  = "logical-backup.json"
	ManifestFile = "deployment-manifest.json"
	MetadataFile = "backup-metadata.json"
)

type Metadata struct {
	SchemaVersion         int             `json:"schema_version"`
	CreatedAt             time.Time       `json:"created_at"`
	DeploymentName        string          `json:"deployment_name"`
	ApplicationVersion    string          `json:"application_version"`
	DatabaseSchemaVersion int             `json:"database_schema_version"`
	DatabaseName          string          `json:"database_name"`
	DatabaseID            string          `json:"database_id"`
	DatabaseBinding       string          `json:"database_binding"`
	Counts                map[string]int  `json:"counts"`
	LogicalDataSHA256     string          `json:"logical_data_sha256"`
	DatabaseRowsSHA256    string          `json:"database_rows_sha256"`
	TableRows             map[string]int  `json:"table_rows"`
	Files                 map[string]File `json:"files"`
}

type File struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Input struct {
	DatabasePath string
	LogicalPath  string
	ManifestPath string
	Metadata     Metadata
}

type Extracted struct {
	Directory string
	Database  string
	Logical   string
	Manifest  string
	Metadata  Metadata
}

func CreateEncrypted(output, passphrase string, input Input) error {
	if passphrase == "" {
		return errors.New("backup passphrase is required")
	}
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("backup output already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	paths := map[string]string{DatabaseFile: input.DatabasePath, LogicalFile: input.LogicalPath, ManifestFile: input.ManifestPath}
	metadata := input.Metadata
	metadata.SchemaVersion = SchemaVersion
	metadata.CreatedAt = time.Now().UTC()
	metadata.Files = make(map[string]File, len(paths))
	for name, path := range paths {
		info, digest, err := inspectFile(path)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
		metadata.Files[name] = File{Size: info.Size(), SHA256: digest}
	}
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".misub-backup-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	encrypted, err := age.Encrypt(temporary, recipient)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(encrypted)
	archive := tar.NewWriter(compressed)
	for _, name := range []string{DatabaseFile, LogicalFile, ManifestFile} {
		if err := addFile(archive, name, paths[name], metadata.Files[name]); err != nil {
			return err
		}
	}
	if err := addBytes(archive, MetadataFile, append(metadataJSON, '\n')); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := encrypted.Close(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := copyExclusive(temporaryPath, output); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(output)
		return err
	}
	committed = true
	return nil
}

func copyExclusive(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = output.Close()
		if !keep {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	keep = true
	return nil
}

func ExtractEncrypted(inputPath, outputDir, passphrase string) (Extracted, error) {
	if passphrase == "" {
		return Extracted{}, errors.New("backup passphrase is required")
	}
	input, err := os.Open(inputPath)
	if err != nil {
		return Extracted{}, err
	}
	defer input.Close()
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return Extracted{}, err
	}
	decrypted, err := age.Decrypt(input, identity)
	if err != nil {
		return Extracted{}, err
	}
	compressed, err := gzip.NewReader(decrypted)
	if err != nil {
		return Extracted{}, err
	}
	defer compressed.Close()
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Extracted{}, err
	}
	allowed := map[string]int64{DatabaseFile: 5 << 30, LogicalFile: 64 << 20, ManifestFile: 16 << 20, MetadataFile: 16 << 20}
	created := make(map[string]string, len(allowed))
	cleanup := func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
	}
	archive := tar.NewReader(compressed)
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			cleanup()
			return Extracted{}, nextErr
		}
		limit, ok := allowed[header.Name]
		if !ok || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > limit {
			cleanup()
			return Extracted{}, fmt.Errorf("backup contains invalid archive entry %q", header.Name)
		}
		if _, duplicate := created[header.Name]; duplicate {
			cleanup()
			return Extracted{}, fmt.Errorf("backup contains duplicate entry %q", header.Name)
		}
		path := filepath.Join(outputDir, header.Name)
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			cleanup()
			return Extracted{}, openErr
		}
		_, copyErr := io.CopyN(file, archive, header.Size)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			cleanup()
			return Extracted{}, errors.Join(copyErr, closeErr)
		}
		created[header.Name] = path
	}
	for name := range allowed {
		if created[name] == "" {
			cleanup()
			return Extracted{}, fmt.Errorf("backup is missing %s", name)
		}
	}
	metadataJSON, err := os.ReadFile(created[MetadataFile])
	if err != nil {
		cleanup()
		return Extracted{}, err
	}
	var metadata Metadata
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		cleanup()
		return Extracted{}, err
	}
	if err := validateMetadata(metadata); err != nil {
		cleanup()
		return Extracted{}, err
	}
	for name, expected := range metadata.Files {
		info, digest, err := inspectFile(created[name])
		if err != nil || info.Size() != expected.Size || digest != expected.SHA256 {
			cleanup()
			return Extracted{}, fmt.Errorf("backup file integrity check failed for %s", name)
		}
	}
	return Extracted{Directory: outputDir, Database: created[DatabaseFile], Logical: created[LogicalFile], Manifest: created[ManifestFile], Metadata: metadata}, nil
}

func validateMetadata(value Metadata) error {
	if value.SchemaVersion != SchemaVersion || value.DeploymentName == "" || value.DatabaseName == "" || value.DatabaseID == "" || value.DatabaseBinding != "MISUB_DB" {
		return errors.New("backup metadata is incomplete")
	}
	if value.Counts == nil || value.TableRows == nil || !validDigest(value.LogicalDataSHA256) || !validDigest(value.DatabaseRowsSHA256) || len(value.Files) != 3 {
		return errors.New("backup metadata is missing counts, logical checksum, or file metadata")
	}
	for name, file := range value.Files {
		if file.Size <= 0 || !validDigest(file.SHA256) || (name != DatabaseFile && name != LogicalFile && name != ManifestFile) {
			return errors.New("backup metadata contains invalid file metadata")
		}
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func inspectFile(path string) (os.FileInfo, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if info.Size() == 0 {
		return info, "", errors.New("file is empty")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, "", err
	}
	return info, hex.EncodeToString(hash.Sum(nil)), nil
}

func addFile(archive *tar.Writer, name, path string, expected File) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: expected.Size}); err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(file, expected.Size+1))
	if err != nil {
		return err
	}
	if written != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("source file changed while creating backup: %s", name)
	}
	return nil
}

func addBytes(archive *tar.Writer, name string, data []byte) error {
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := archive.Write(data)
	return err
}
