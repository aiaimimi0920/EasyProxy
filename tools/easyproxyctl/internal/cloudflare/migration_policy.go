package cloudflare

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// MigrationPlan only contains the two reviewed, expand-only MiSub migrations.
// New SQL requires an explicit policy review, not a configurable bypass.
type MigrationPlan struct {
	Statements []string
}

var reviewedMigrations = []struct{ name, sha256 string }{
	{"0001_initial.sql", "50a4e7c1c5246dd52ee94c3130ef46450229c7b8b86505cf40152a0a90a9076f"},
	{"0002_cron_executions.sql", "cbc96d535f18200955567b7ec625df6a656f7c24d617e954445942c0fb7362ff"},
}

func LoadReviewedMigrations(directory string) (MigrationPlan, error) {
	reject := errors.New("EASYPROXY_MIGRATION_POLICY_REJECTED")
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return MigrationPlan{}, reject
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return MigrationPlan{}, reject
	}
	allowed := map[string]bool{"README.md": true}
	for _, expected := range reviewedMigrations {
		allowed[expected.name] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return MigrationPlan{}, reject
		}
	}
	var plan MigrationPlan
	for _, expected := range reviewedMigrations {
		path := filepath.Join(directory, expected.name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
			return MigrationPlan{}, reject
		}
		body, err := os.ReadFile(path)
		digest := sha256.Sum256(body)
		if err != nil || hex.EncodeToString(digest[:]) != expected.sha256 {
			return MigrationPlan{}, reject
		}
		// Splitting is only safe after the exact reviewed bytes have matched.
		for _, statement := range strings.Split(string(body), ";") {
			if statement = strings.TrimSpace(statement); statement != "" {
				plan.Statements = append(plan.Statements, statement)
			}
		}
	}
	return plan, nil
}
