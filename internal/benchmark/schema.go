package benchmark

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

func ValidateExpectedSchema(data []byte) error {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type     string          `json:"type"`
			Const    json.RawMessage `json:"const"`
			Pattern  string          `json:"pattern"`
			MinItems *int            `json:"minItems"`
		} `json:"properties"`
		AdditionalProperties *bool `json:"additionalProperties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return fmt.Errorf("decode expected schema: %w", err)
	}
	if len(schema.Required) == 0 || len(schema.Properties) == 0 || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		return errors.New("expected schema must define required typed properties and reject unknown fields")
	}
	for _, name := range schema.Required {
		if schema.Properties[name].Type == "" {
			return fmt.Errorf("expected schema required property %q has no type", name)
		}
	}
	return nil
}

func ValidateScenarioSchema(s Scenario, expectedSchema []byte) error {
	if err := ValidateExpectedSchema(expectedSchema); err != nil {
		return err
	}
	hash := sha256.Sum256(bytes.ReplaceAll(expectedSchema, []byte("\r\n"), []byte("\n")))
	if s.SchemaSHA256 != hex.EncodeToString(hash[:]) {
		return errors.New("scenario schema_sha256 does not match expected-schema.json")
	}
	return nil
}

func ValidateReportAgainstSchema(report Report, expectedSchema []byte) error {
	if err := ValidateExpectedSchema(expectedSchema); err != nil {
		return err
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type     string          `json:"type"`
			Const    json.RawMessage `json:"const"`
			Pattern  string          `json:"pattern"`
			MinItems *int            `json:"minItems"`
		} `json:"properties"`
		AdditionalProperties bool `json:"additionalProperties"`
	}
	if err := json.Unmarshal(expectedSchema, &schema); err != nil {
		return err
	}
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(b, &object); err != nil {
		return err
	}
	for _, required := range schema.Required {
		if _, ok := object[required]; !ok {
			return fmt.Errorf("report missing required property %q", required)
		}
	}
	if !schema.AdditionalProperties {
		for key := range object {
			if _, ok := schema.Properties[key]; !ok {
				return fmt.Errorf("report contains unknown property %q", key)
			}
		}
	}
	for key, definition := range schema.Properties {
		raw, ok := object[key]
		if !ok {
			continue
		}
		var target interface{}
		switch definition.Type {
		case "string":
			target = new(string)
		case "array":
			target = new([]json.RawMessage)
		case "object":
			target = new(map[string]json.RawMessage)
		case "boolean":
			target = new(bool)
		case "integer":
			target = new(int64)
		case "number":
			target = new(float64)
		default:
			return fmt.Errorf("unsupported expected schema type %q for %s", definition.Type, key)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("report property %q has wrong type: %w", key, err)
		}
		if len(definition.Const) > 0 && !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(definition.Const)) {
			return fmt.Errorf("report property %q does not match const", key)
		}
		if definition.Pattern != "" && definition.Type == "string" {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("report property %q has invalid string: %w", key, err)
			}
			matched, err := regexp.MatchString(definition.Pattern, value)
			if err != nil || !matched {
				return fmt.Errorf("report property %q does not match pattern", key)
			}
		}
		if definition.MinItems != nil && definition.Type == "array" {
			var values []json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil || len(values) < *definition.MinItems {
				return fmt.Errorf("report property %q has fewer than minItems", key)
			}
		}
	}
	return nil
}
