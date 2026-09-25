package clocks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// matchEventSchema checks an outbox payload against contracts/events/event.schema.json.
func matchEventSchema(schemaDoc, document []byte) error {
	var sch map[string]any
	if err := json.Unmarshal(schemaDoc, &sch); err != nil {
		return fmt.Errorf("clocks: event schema: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(document))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("clocks: event json: %w", err)
	}
	defs, _ := sch["$defs"].(map[string]any)
	if err := checkSchema(sch, defs, doc); err != nil {
		return fmt.Errorf("clocks: event schema: %w", err)
	}
	return nil
}

func checkSchema(sch, defs map[string]any, v any) error {
	if ref, ok := sch["$ref"].(string); ok {
		resolved, err := resolveRef(ref, defs)
		if err != nil {
			return err
		}
		return checkSchema(resolved, defs, v)
	}
	if branches, ok := sch["oneOf"].([]any); ok {
		matched := 0
		for _, branch := range branches {
			sub, ok := branch.(map[string]any)
			if !ok {
				return fmt.Errorf("oneOf branch is not an object")
			}
			if err := checkSchema(sub, defs, v); err == nil {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("oneOf matched %d branches", matched)
		}
		return nil
	}
	if spec, ok := sch["type"]; ok && !typeMatches(spec, v) {
		return fmt.Errorf("type mismatch")
	}
	if enum, ok := sch["enum"].([]any); ok {
		if !enumContains(enum, v) {
			return fmt.Errorf("value %v is not in enum", v)
		}
	}
	if pat, ok := sch["pattern"].(string); ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("pattern on non-string")
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return err
		}
		if !re.MatchString(s) {
			return fmt.Errorf("value %q does not match %s", s, pat)
		}
	}
	if format, ok := sch["format"].(string); ok && format == "date-time" {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("date-time on non-string")
		}
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			return fmt.Errorf("date-time: %w", err)
		}
	}
	if minLen, ok := sch["minLength"].(float64); ok {
		s, ok := v.(string)
		if !ok || len(s) < int(minLen) {
			return fmt.Errorf("minLength")
		}
	}
	if minimum, ok := sch["minimum"].(float64); ok {
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("minimum on non-number")
		}
		f, err := n.Float64()
		if err != nil || f < minimum {
			return fmt.Errorf("minimum")
		}
	}
	if req, ok := sch["required"].([]any); ok {
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("required on non-object")
		}
		for _, key := range req {
			name, _ := key.(string)
			if _, present := obj[name]; !present {
				return fmt.Errorf("missing %s", name)
			}
		}
	}
	if props, ok := sch["properties"].(map[string]any); ok {
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("properties on non-object")
		}
		if sch["additionalProperties"] == false {
			for key := range obj {
				if _, known := props[key]; !known {
					return fmt.Errorf("unexpected property %s", key)
				}
			}
		}
		for key, raw := range obj {
			sub, known := props[key].(map[string]any)
			if !known {
				continue
			}
			if err := checkSchema(sub, defs, raw); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	if items, ok := sch["items"].(map[string]any); ok {
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("items on non-array")
		}
		seen := map[string]bool{}
		unique, _ := sch["uniqueItems"].(bool)
		for _, item := range arr {
			if err := checkSchema(items, defs, item); err != nil {
				return err
			}
			if unique {
				b, err := json.Marshal(item)
				if err != nil {
					return err
				}
				if seen[string(b)] {
					return fmt.Errorf("duplicate item")
				}
				seen[string(b)] = true
			}
		}
	}
	return nil
}

func resolveRef(ref string, defs map[string]any) (map[string]any, error) {
	const prefix = "#/$defs/"
	if len(ref) <= len(prefix) || ref[:len(prefix)] != prefix {
		return nil, fmt.Errorf("unsupported ref %s", ref)
	}
	m, ok := defs[ref[len(prefix):]].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing %s", ref)
	}
	return m, nil
}

func typeMatches(spec, v any) bool {
	switch s := spec.(type) {
	case string:
		return jsonType(v) == s || (s == "integer" && isInteger(v))
	case []any:
		for _, item := range s {
			name, _ := item.(string)
			if name == "integer" && isInteger(v) {
				return true
			}
			if jsonType(v) == name {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return ""
	}
}

func isInteger(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	if _, err := n.Int64(); err != nil {
		return false
	}
	return true
}

func enumContains(enum []any, v any) bool {
	for _, item := range enum {
		if item == nil && v == nil {
			return true
		}
		if item == v {
			return true
		}
		if n, ok := v.(json.Number); ok {
			if f, ok := item.(float64); ok {
				got, err := n.Float64()
				if err == nil && got == f {
					return true
				}
			}
		}
	}
	return false
}
