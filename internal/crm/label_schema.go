package crm

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
)

const labelConfigName = "transcript-labels"

//go:embed transcript-labels.json
var defaultLabelConfig []byte

type labelDimension struct {
	Key         string   `json:"key"`
	Group       string   `json:"group"`
	Speaker     string   `json:"speaker,omitempty"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Options     []string `json:"options,omitempty"`
}

type labelSchema struct {
	Enabled      bool             `json:"enabled"`
	ReturnSchema json.RawMessage  `json:"return_schema,omitempty"`
	Prompt       string           `json:"prompt"`
	Dimensions   []labelDimension `json:"dimensions"`
}

func strictLabelJSON(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func parseLabelSchema(raw []byte) (labelSchema, error) {
	var schema labelSchema
	if err := strictLabelJSON(raw, &schema); err != nil || strings.TrimSpace(schema.Prompt) == "" || len(schema.Prompt) > 16384 {
		return schema, errors.New("provide a prompt and 1–32 dimensions")
	}
	return schema, schema.validateDimensions()
}

func (schema labelSchema) validateDimensions() error {
	if len(schema.Dimensions) == 0 || len(schema.Dimensions) > 32 {
		return errors.New("provide 1–32 return fields")
	}
	seen := map[string]bool{}
	for _, d := range schema.Dimensions {
		if !leadKey.MatchString(d.Key) || seen[d.Key] || !slices.Contains([]string{"labels", "user", "vehicle"}, d.Group) || !slices.Contains([]string{"string", "number", "boolean", "enum"}, d.Type) || strings.TrimSpace(d.Description) == "" || len(d.Description) > 1024 {
			return errors.New("each dimension needs a unique key, group, supported type and description")
		}
		if d.Speaker != "" && d.Speaker != "user" && d.Speaker != "assistant" {
			return errors.New("dimension speaker must be user or assistant")
		}
		seen[d.Key] = true
		if (d.Type == "enum") != (len(d.Options) > 0) || len(d.Options) > 64 {
			return errors.New("only enums take options and require at least one")
		}
		options := map[string]bool{}
		for _, v := range d.Options {
			if strings.TrimSpace(v) == "" || len(v) > 256 || options[v] {
				return errors.New("enum options must be distinct nonempty strings of at most 256 bytes")
			}
			options[v] = true
		}
	}
	return nil
}

type labelTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type observation struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
	Turn  int             `json:"turn"`
	Quote string          `json:"quote"`
	Role  string          `json:"role,omitempty"`
}

func (d labelDimension) accepts(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) != "" && len(v) <= 1024 && (d.Type == "string" || d.Type == "enum" && slices.Contains(d.Options, v))
	case bool:
		return d.Type == "boolean"
	case json.Number:
		return d.Type == "number"
	}
	return false
}

func validateLabels(raw string, schema labelSchema, turns []labelTurn, vehicleID string) ([]observation, string, error) {
	var answer struct {
		Observations []observation `json:"observations"`
	}
	if err := strictLabelJSON([]byte(raw), &answer); err != nil || answer.Observations == nil {
		return nil, "", fail(502, "label model returned an invalid JSON envelope")
	}
	dims := map[string]labelDimension{}
	groups := map[string]any{}
	if vehicleID != "" {
		groups["vehicle_id"] = vehicleID
	}
	for _, d := range schema.Dimensions {
		dims[d.Key] = d
	}
	for i := range answer.Observations {
		o := &answer.Observations[i]
		d, ok := dims[o.Key]
		var v any
		if !ok || o.Role != "" || strictLabelJSON(o.Value, &v) != nil || !d.accepts(v) {
			return nil, "", fail(502, "label model returned an undeclared, duplicate or mistyped field")
		}
		if o.Turn < 1 || o.Turn > len(turns) {
			return nil, "", fail(502, "label model cited an unknown turn")
		}
		turn := turns[o.Turn-1]
		if (d.Speaker != "" && turn.Role != d.Speaker) || strings.TrimSpace(o.Quote) == "" || len(o.Quote) > 1024 || !strings.Contains(turn.Content, o.Quote) {
			return nil, "", fail(502, "label evidence does not match the required speaker and turn")
		}
		o.Role = turn.Role
		delete(dims, o.Key)
		fields, ok := groups[d.Group].(map[string]any)
		if !ok {
			fields = map[string]any{}
			groups[d.Group] = fields
		}
		fields[d.Key] = o.Value
	}
	value, err := json.Marshal(groups)
	if err != nil || len(value) > 4096 {
		return nil, "", fail(502, "labeled values exceed the memory fact limit")
	}
	return answer.Observations, string(value), nil
}

// The same dimensions drive the model's JSON Schema and the evidence validator.
func (s labelSchema) outputSchema() map[string]any {
	variants := make([]map[string]any, 0, len(s.Dimensions))
	for _, d := range s.Dimensions {
		value := map[string]any{"type": d.Type}
		if d.Type == "enum" {
			value = map[string]any{"type": "string", "enum": d.Options}
		}
		props := map[string]any{"key": map[string]string{"const": d.Key}, "value": value, "turn": map[string]any{"type": "integer", "minimum": 1}, "quote": map[string]any{"type": "string", "minLength": 1}}
		variants = append(variants, map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": []string{"key", "value", "turn", "quote"}})
	}
	array := map[string]any{"type": "array", "maxItems": len(variants), "items": map[string]any{"oneOf": variants}}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"observations"}, "properties": map[string]any{"observations": array}}
}

func labelTaskConfig(raw []byte) ([]byte, error) {
	s, err := parseLabelSchema(raw)
	if err != nil {
		return nil, err
	}
	s.ReturnSchema, _ = json.Marshal(s.outputSchema())
	return json.Marshal(s)
}

func previewLabelSchema(w http.ResponseWriter, r *http.Request) error {
	var raw json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		return err
	}
	var schema labelSchema
	if err := strictLabelJSON(raw, &schema.Dimensions); err != nil {
		return fail(http.StatusBadRequest, "return fields must be a JSON array of field definitions: %s", err)
	}
	if err := schema.validateDimensions(); err != nil {
		return fail(http.StatusBadRequest, "%s", err)
	}
	writeJSON(w, http.StatusOK, schema.outputSchema())
	return nil
}
