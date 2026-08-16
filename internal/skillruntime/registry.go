package skillruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
	"go.yaml.in/yaml/v3"
)

type Registry struct {
	root        string
	definitions map[string]map[string]Definition
}

var canonicalSkillNames = map[string]struct{}{
	"plan": {}, "context": {}, "record": {}, "history": {}, "verify": {}, "export": {}, "import": {},
	"advisory": {}, "mirror": {}, "patch": {}, "audit": {},
}

type definitionDocument struct {
	Name                  string                `yaml:"name"`
	Version               string                `yaml:"version"`
	InputSchema           string                `yaml:"input_schema"`
	OutputSchema          string                `yaml:"output_schema"`
	Risk                  RiskLevel             `yaml:"risk"`
	AllowedFunctions      []model.AgentFunction `yaml:"allowed_functions"`
	RequiredContext       bool                  `yaml:"required_context"`
	RequiredLease         bool                  `yaml:"required_lease"`
	SupportedEnvironments []string              `yaml:"supported_environments"`
	Adapter               string                `yaml:"adapter"`
	Timeout               string                `yaml:"timeout"`
	RetryPolicy           string                `yaml:"retry_policy"`
	EvidencePolicy        string                `yaml:"evidence_policy"`
}

func Load(root string) (*Registry, error) {
	canonicalRoot, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	registry := &Registry{root: canonicalRoot, definitions: make(map[string]map[string]Definition)}
	err = filepath.WalkDir(canonicalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return nil
		}
		definition, err := LoadFile(canonicalRoot, path)
		if err != nil {
			return err
		}
		if registry.definitions[definition.Name] == nil {
			registry.definitions[definition.Name] = make(map[string]Definition)
		}
		if _, exists := registry.definitions[definition.Name][definition.Version]; exists {
			return fmt.Errorf("duplicate canonical skill %q version %q", definition.Name, definition.Version)
		}
		registry.definitions[definition.Name][definition.Version] = definition
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := validateCanonicalSkillSet(registry.definitions); err != nil {
		return nil, err
	}
	return registry, nil
}

func LoadFile(root, path string) (Definition, error) {
	canonicalRoot, err := canonicalRoot(root)
	if err != nil {
		return Definition{}, err
	}
	canonicalPath, err := filepath.Abs(path)
	if err != nil {
		return Definition{}, err
	}
	canonicalPath, err = filepath.EvalSymlinks(canonicalPath)
	if err != nil {
		return Definition{}, err
	}
	if !withinRoot(canonicalRoot, canonicalPath) {
		return Definition{}, errors.New("skill definition is outside registry root")
	}
	data, err := os.ReadFile(canonicalPath)
	if err != nil {
		return Definition{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document definitionDocument
	if err := decoder.Decode(&document); err != nil {
		return Definition{}, fmt.Errorf("decode %q: %w", canonicalPath, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Definition{}, fmt.Errorf("decode %q: multiple YAML documents are not allowed", canonicalPath)
		}
		return Definition{}, fmt.Errorf("decode %q: %w", canonicalPath, err)
	}
	return validateDocument(document)
}

func (registry *Registry) Resolve(name, version string) (Definition, error) {
	versions, exists := registry.definitions[strings.TrimSpace(name)]
	if !exists {
		return Definition{}, fmt.Errorf("canonical skill %q not found", name)
	}
	version = strings.TrimSpace(version)
	if version == "" {
		if len(versions) != 1 {
			return Definition{}, fmt.Errorf("canonical skill %q has no unambiguous current version", name)
		}
		for _, definition := range versions {
			return cloneDefinition(definition), nil
		}
	}
	definition, exists := versions[version]
	if !exists {
		return Definition{}, fmt.Errorf("canonical skill %q version %q not found", name, version)
	}
	return cloneDefinition(definition), nil
}

func (registry *Registry) Definitions() []Definition {
	definitions := make([]Definition, 0)
	for _, versions := range registry.definitions {
		for _, definition := range versions {
			definitions = append(definitions, cloneDefinition(definition))
		}
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].Name == definitions[j].Name {
			return definitions[i].Version < definitions[j].Version
		}
		return definitions[i].Name < definitions[j].Name
	})
	return definitions
}

// ValidateJSONInput verifies a tool payload against its registry-owned input schema.
func ValidateJSONInput(definition Definition, input json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return errors.New("input must be valid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("input must contain exactly one JSON value")
	}
	var schema map[string]any
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		return errors.New("registry input schema is invalid")
	}
	return validateJSONValue(schema, value)
}

func canonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("registry root must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func withinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validateDocument(document definitionDocument) (Definition, error) {
	definition := Definition{
		Name: strings.TrimSpace(document.Name), Version: strings.TrimSpace(document.Version), Risk: document.Risk,
		AllowedFunctions: append([]model.AgentFunction(nil), document.AllowedFunctions...), RequiredContext: document.RequiredContext,
		RequiredLease: document.RequiredLease, SupportedEnvironments: trimStrings(document.SupportedEnvironments),
		Adapter: strings.TrimSpace(document.Adapter), Timeout: strings.TrimSpace(document.Timeout),
		RetryPolicy: strings.TrimSpace(document.RetryPolicy), EvidencePolicy: strings.TrimSpace(document.EvidencePolicy),
	}
	if definition.Name == "" || definition.Version == "" || definition.Adapter == "" || definition.Timeout == "" || definition.RetryPolicy == "" || definition.EvidencePolicy == "" {
		return Definition{}, errors.New("skill name, version, adapter, timeout, retry policy, and evidence policy are required")
	}
	if !model.IsValidRiskLevel(string(definition.Risk)) {
		return Definition{}, errors.New("skill risk level is invalid")
	}
	if len(definition.AllowedFunctions) == 0 || len(definition.SupportedEnvironments) == 0 || hasBlankFunctions(definition.AllowedFunctions) || hasBlankStrings(definition.SupportedEnvironments) {
		return Definition{}, errors.New("skill allowed functions and supported environments are required")
	}
	inputSchema, err := validateJSONObjectSchema(document.InputSchema)
	if err != nil {
		return Definition{}, fmt.Errorf("input schema: %w", err)
	}
	outputSchema, err := validateJSONObjectSchema(document.OutputSchema)
	if err != nil {
		return Definition{}, fmt.Errorf("output schema: %w", err)
	}
	definition.InputSchema = inputSchema
	definition.OutputSchema = outputSchema
	return definition, nil
}

func validateJSONObjectSchema(value string) (json.RawMessage, error) {
	var valueNode any
	if err := json.Unmarshal([]byte(value), &valueNode); err != nil {
		return nil, errors.New("must be valid JSON object schema")
	}
	if err := validateSchemaNode(valueNode, true); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), []byte(value)...), nil
}

func validateSchemaNode(value any, root bool) error {
	schema, ok := value.(map[string]any)
	if !ok || schema == nil {
		return errors.New("schema must be an object")
	}
	for key := range schema {
		switch key {
		case "$schema", "type", "properties", "required", "items", "additionalProperties", "enum", "const", "description":
		default:
			return fmt.Errorf("unsupported schema keyword %q", key)
		}
	}
	if rawType, exists := schema["type"]; exists {
		typeName, ok := rawType.(string)
		if !ok || !validJSONType(typeName) {
			return errors.New("schema type must be a valid JSON type")
		}
		if root && typeName != "object" {
			return errors.New("must declare type object")
		}
	} else if root {
		return errors.New("must declare type object")
	}
	if rawSchema, exists := schema["$schema"]; exists {
		if _, ok := rawSchema.(string); !ok {
			return errors.New("$schema must be a string")
		}
	}
	if rawDescription, exists := schema["description"]; exists {
		if _, ok := rawDescription.(string); !ok {
			return errors.New("description must be a string")
		}
	}
	if rawProperties, exists := schema["properties"]; exists {
		properties, ok := rawProperties.(map[string]any)
		if !ok || properties == nil {
			return errors.New("properties must be an object")
		}
		for name, property := range properties {
			if strings.TrimSpace(name) == "" {
				return errors.New("property names cannot be blank")
			}
			if err := validateSchemaNode(property, false); err != nil {
				return fmt.Errorf("property %q: %w", name, err)
			}
		}
	}
	if rawRequired, exists := schema["required"]; exists {
		required, ok := rawRequired.([]any)
		if !ok {
			return errors.New("required must be a string array")
		}
		seen := make(map[string]struct{}, len(required))
		for _, value := range required {
			name, ok := value.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return errors.New("required must be a string array")
			}
			if _, exists := seen[name]; exists {
				return errors.New("required cannot contain duplicates")
			}
			seen[name] = struct{}{}
		}
	}
	if rawItems, exists := schema["items"]; exists {
		if err := validateSchemaNode(rawItems, false); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	if rawAdditional, exists := schema["additionalProperties"]; exists {
		if _, ok := rawAdditional.(bool); !ok {
			if err := validateSchemaNode(rawAdditional, false); err != nil {
				return fmt.Errorf("additionalProperties: %w", err)
			}
		}
	}
	if rawEnum, exists := schema["enum"]; exists {
		values, ok := rawEnum.([]any)
		if !ok || len(values) == 0 {
			return errors.New("enum must be a non-empty array")
		}
	}
	return nil
}

func validateJSONValue(schema map[string]any, value any) error {
	if expected, exists := schema["const"]; exists && !reflect.DeepEqual(expected, value) {
		return errors.New("input does not match schema const")
	}
	if values, exists := schema["enum"].([]any); exists {
		matched := false
		for _, candidate := range values {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("input does not match schema enum")
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("input must be an object")
		}
		properties, _ := schema["properties"].(map[string]any)
		if required, exists := schema["required"].([]any); exists {
			for _, rawName := range required {
				name, _ := rawName.(string)
				if _, exists := object[name]; !exists {
					return fmt.Errorf("input missing required property %q", name)
				}
			}
		}
		for name, candidate := range object {
			rawProperty, known := properties[name]
			if !known {
				if additional, exists := schema["additionalProperties"]; exists {
					if allowed, ok := additional.(bool); ok && !allowed {
						return fmt.Errorf("input property %q is not allowed", name)
					}
					if nested, ok := additional.(map[string]any); ok {
						if err := validateJSONValue(nested, candidate); err != nil {
							return fmt.Errorf("input property %q: %w", name, err)
						}
					}
				}
				continue
			}
			property, ok := rawProperty.(map[string]any)
			if !ok {
				return errors.New("registry property schema is invalid")
			}
			if err := validateJSONValue(property, candidate); err != nil {
				return fmt.Errorf("input property %q: %w", name, err)
			}
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return errors.New("input must be an array")
		}
		if rawItems, exists := schema["items"]; exists {
			items, ok := rawItems.(map[string]any)
			if !ok {
				return errors.New("registry item schema is invalid")
			}
			for index, candidate := range values {
				if err := validateJSONValue(items, candidate); err != nil {
					return fmt.Errorf("input item %d: %w", index, err)
				}
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return errors.New("input must be a string")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("input must be a boolean")
		}
	case "number":
		if _, ok := value.(json.Number); !ok {
			return errors.New("input must be a number")
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok || strings.ContainsAny(number.String(), ".eE") {
			return errors.New("input must be an integer")
		}
	case "null":
		if value != nil {
			return errors.New("input must be null")
		}
	}
	return nil
}

func validJSONType(value string) bool {
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func validateCanonicalSkillSet(definitions map[string]map[string]Definition) error {
	if len(definitions) != len(canonicalSkillNames) {
		return errors.New("registry must contain exactly eleven canonical skill names")
	}
	for name := range canonicalSkillNames {
		versions, exists := definitions[name]
		if !exists || len(versions) != 1 {
			return fmt.Errorf("registry must contain exactly one version of canonical skill %q", name)
		}
	}
	for name := range definitions {
		if _, exists := canonicalSkillNames[name]; !exists {
			return fmt.Errorf("registry contains non-canonical skill %q", name)
		}
	}
	return nil
}

func cloneDefinition(definition Definition) Definition {
	definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	definition.OutputSchema = append(json.RawMessage(nil), definition.OutputSchema...)
	definition.AllowedFunctions = append([]model.AgentFunction(nil), definition.AllowedFunctions...)
	definition.SupportedEnvironments = append([]string(nil), definition.SupportedEnvironments...)
	return definition
}

func trimStrings(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	return result
}

func hasBlankStrings(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func hasBlankFunctions(values []model.AgentFunction) bool {
	for _, value := range values {
		if strings.TrimSpace(string(value)) == "" {
			return true
		}
	}
	return false
}
