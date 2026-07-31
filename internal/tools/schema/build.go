package schema

import (
	"errors"
	"fmt"
)

/* build initialize for node interface.*/
func (i integarNode) build() (map[string]any, error) {
	return map[string]any{
		"type":        "integer",
		"description": i.description,
	}, nil
}

func (b booleanNode) build() (map[string]any, error) {
	return map[string]any{
		"type":        "boolean",
		"description": b.description,
	}, nil
}

func (a arrayNode) build() (map[string]any, error) {
	if a.item == nil { //todo
		return nil, errors.New("array item schema is nil")
	}
	item, err := a.item.build()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":        "array",
		"description": a.description,
		"items":       item,
	}, nil
}

func (s stringNode) build() (map[string]any, error) {
	out := map[string]any{"type": "string"}
	if s.decription != "" {
		out["description"] = s.decription
	}
	if len(s.enum) > 0 {
		seen := map[string]bool{}
		for _, unit := range s.enum {
			if unit == "" {
				return nil, errors.New("enum contains empty value")
			}
			if seen[unit] {
				return nil, fmt.Errorf("duplicate enum %q", unit)
			}
			seen[unit] = true
		}
		out["enum"] = append([]string(nil), s.enum...)
	}
	return out, nil
}

/* object build.*/
func (o objectNode) build() (map[string]any, error) {
	/* corresponding to openai struct.*/
	prop := map[string]any{}
	required := []string{}
	if len(o.fields) > 0 {
		for _, field := range o.fields {
			if field.Name == "" {
				return nil, errors.New("object field name is empty")
			}
			if field.Schema == nil {
				return nil, fmt.Errorf("field %q has nil schema", field.Name)
			}
			if _, ok := prop[field.Name]; ok {
				return nil, fmt.Errorf("duplicate field %q", field.Name)
			}
			/* recurse the build function and finally a json tree.*/
			child, err := field.Schema.build()
			if err != nil {
				return nil, err
			}
			prop[field.Name] = child
			if field.Required {
				required = append(required, field.Name)
			}
		}
	}
	out := map[string]any{
		"type":                 "object",
		"properties":           prop,
		"additionalProperties": o.additionalBoolProperty,
	}

	if len(required) > 0 {
		out["required"] = required
	}

	return out, nil
}
