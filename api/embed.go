// Package api contains the embedded ProjectLens API contract.
package api

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed openapi.yaml
var openAPIYAML string

func OpenAPIYAML() []byte {
	return []byte(openAPIYAML)
}

// OpenAPIJSON converts the embedded YAML to JSON using sorted JSON map keys.
func OpenAPIJSON() ([]byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(openAPIYAML), &document); err != nil {
		return nil, fmt.Errorf("parse embedded OpenAPI document: %w", err)
	}
	return json.Marshal(document)
}
