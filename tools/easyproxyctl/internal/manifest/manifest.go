package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

const SchemaVersion = 1

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type Manifest struct {
	SchemaVersion  int               `json:"schema_version"`
	DeploymentName string            `json:"deployment_name"`
	ReleaseChannel string            `json:"release_channel"`
	GeneratedAt    time.Time         `json:"generated_at"`
	WorkflowRun    string            `json:"workflow_run,omitempty"`
	TopologySHA256 string            `json:"topology_sha256"`
	Components     []string          `json:"components"`
	Resources      []Resource        `json:"resources"`
	Source         Source            `json:"source"`
	Images         map[string]string `json:"images"`
	Checksum       string            `json:"checksum"`
}

type Resource struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	ID            string `json:"id,omitempty"`
	URL           string `json:"url,omitempty"`
	SchemaVersion string `json:"schema_version,omitempty"`
}

type Source struct {
	RootCommit       string            `json:"root_commit,omitempty"`
	SubmoduleCommits map[string]string `json:"submodule_commits"`
}

func (m *Manifest) Seal() error {
	if err := m.validateContent(); err != nil {
		return err
	}
	sort.Slice(m.Resources, func(i, j int) bool {
		if m.Resources[i].Kind == m.Resources[j].Kind {
			return m.Resources[i].Name < m.Resources[j].Name
		}
		return m.Resources[i].Kind < m.Resources[j].Kind
	})
	sort.Strings(m.Components)
	digest, err := m.contentChecksum()
	if err != nil {
		return err
	}
	m.Checksum = digest
	return nil
}

func (m *Manifest) Verify() error {
	if err := m.validateContent(); err != nil {
		return err
	}
	if len(m.Checksum) != sha256.Size*2 {
		return errors.New("manifest checksum is missing or malformed")
	}
	want, err := m.contentChecksum()
	if err != nil {
		return err
	}
	if m.Checksum != want {
		return errors.New("manifest checksum mismatch")
	}
	return nil
}

func (m *Manifest) contentChecksum() (string, error) {
	copy := *m
	copy.Checksum = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode manifest checksum input: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (m *Manifest) validateContent() error {
	if m.SchemaVersion != SchemaVersion || m.DeploymentName == "" || m.ReleaseChannel == "" {
		return errors.New("manifest schema, deployment name, and release channel are required")
	}
	if m.GeneratedAt.IsZero() || len(m.TopologySHA256) != sha256.Size*2 {
		return errors.New("manifest generated_at and topology_sha256 are required")
	}
	if _, err := hex.DecodeString(m.TopologySHA256); err != nil {
		return errors.New("manifest topology_sha256 is malformed")
	}
	if err := validateComponents(m.Components); err != nil {
		return err
	}
	if !commitPattern.MatchString(m.Source.RootCommit) {
		return errors.New("manifest source.root_commit must be a full Git commit")
	}
	if m.Source.SubmoduleCommits == nil || m.Images == nil {
		return errors.New("manifest source.submodule_commits and images must be objects")
	}
	for path, commit := range m.Source.SubmoduleCommits {
		if path == "" || !commitPattern.MatchString(commit) {
			return fmt.Errorf("manifest submodule commit for %q is malformed", path)
		}
	}
	for name, reference := range m.Images {
		if name == "" || reference == "" {
			return errors.New("manifest image names and references must not be empty")
		}
	}
	seen := make(map[string]struct{}, len(m.Resources))
	for _, resource := range m.Resources {
		key := resource.Kind + "\x00" + resource.Name
		if resource.Kind == "" || resource.Name == "" {
			return errors.New("manifest resource kind and name are required")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate manifest resource %s %q", resource.Kind, resource.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateComponents(components []string) error {
	if len(components) == 0 {
		return errors.New("manifest must list at least one enabled component")
	}
	allowed := map[string]struct{}{
		"aggregator": {}, "misub": {}, "ech_worker": {}, "local_easyproxy": {},
	}
	seen := make(map[string]struct{}, len(components))
	for _, component := range components {
		if _, ok := allowed[component]; !ok {
			return fmt.Errorf("manifest component %q is unknown", component)
		}
		if _, exists := seen[component]; exists {
			return fmt.Errorf("duplicate manifest component %q", component)
		}
		seen[component] = struct{}{}
	}
	return nil
}
