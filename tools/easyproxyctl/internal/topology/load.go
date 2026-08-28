package topology

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

const maxTopologyBytes = 1 << 20

func Load(path string) (*Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read topology: %w", err)
	}
	if len(data) > maxTopologyBytes {
		return nil, fmt.Errorf("topology exceeds %d bytes", maxTopologyBytes)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var result Topology
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode topology: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("topology must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing topology content: %w", err)
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func (t *Topology) CanonicalSHA256Input() ([]byte, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("encode canonical topology: %w", err)
	}
	return data, nil
}
