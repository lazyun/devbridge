package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Validate the original model shape before alias conversion can erase fields
// or Go decoding can collapse null, missing and case-folded property names.
// Canonical historical results retain their separate ParseResult contract.
func validateModelResultWire(raw string, value map[string]any) error {
	if err := uniqueModelResultKeys(raw); err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(ResultJSONSchema), &schema); err != nil {
		return err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return err
	}
	if err = resolved.Validate(value); err != nil {
		return fmt.Errorf("Review model result violates output schema: %w", err)
	}
	return nil
}

// JSON Schema sees decoded objects, so duplicate keys need a raw-token check.
// Keys are decoded before comparison, including their JSON escape sequences.
func uniqueModelResultKeys(raw string) error {
	d := json.NewDecoder(strings.NewReader(raw))
	var visit func() error
	visit = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("Review model result contains duplicate JSON key")
				}
				seen[name] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected Review model JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("Review model result must be one JSON object")
	}
	return nil
}
