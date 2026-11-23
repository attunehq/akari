package tsgen

import (
	"strings"
	"testing"
)

// document wraps component schemas in a minimal OpenAPI document.
func document(schemas string) []byte {
	return []byte(`{"openapi":"3.1.0","components":{"schemas":` + schemas + `}}`)
}

func TestGenerateRendersSupportedSubset(t *testing.T) {
	got, err := Generate([]byte(`{
		"openapi": "3.1.0",
		"components": {
			"securitySchemes": {"session": {"type": "apiKey", "in": "cookie", "name": "s"}},
			"schemas": {
				"Thing": {
					"type": "object",
					"additionalProperties": false,
					"required": ["id", "kind", "tags", "parent", "lit", "mixed"],
					"properties": {
						"id": {"type": "integer", "format": "int64", "minimum": 1},
						"kind": {"type": "string", "enum": ["a", "b"]},
						"lit": {"type": "string", "const": "x"},
						"mode": {"type": "string", "default": "fast"},
						"note": {"type": "string"},
						"parent": {"anyOf": [{"$ref": "#/components/schemas/Thing"}, {"type": "null"}]},
						"tags": {"type": "array", "items": {"type": "string"}},
						"mixed": {"type": "array", "items": {"anyOf": [{"type": "number"}, {"type": "null"}]}},
						"attrs": {"type": "object", "additionalProperties": {}},
						"counts": {"type": "object", "additionalProperties": {"type": "number"}},
						"empty": {"type": "object", "additionalProperties": false}
					}
				}
			},
			"responses": {
				"Busy": {
					"description": "Try later.",
					"headers": {"Retry-After": {"required": true, "schema": {"type": "integer"}}},
					"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Thing"}}}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{
		"        Thing: {\n",
		"            /** Format: int64 */\n            id: number;\n",
		`            kind: "a" | "b";` + "\n",
		`            lit: "x";` + "\n",
		// A defaulted property is always present (openapi-typescript's rule).
		"            /** @default \"fast\" */\n            mode: string;\n",
		"            note?: string;\n",
		`            parent: components["schemas"]["Thing"] | null;` + "\n",
		"            tags: string[];\n",
		"            mixed: (number | null)[];\n",
		"            attrs?: {\n                [key: string]: unknown;\n            };\n",
		"            counts?: {\n                [key: string]: number;\n            };\n",
		"            empty?: Record<string, never>;\n",
		"        /** Try later. */\n        Busy: {\n",
		"                \"Retry-After\": number;\n                [name: string]: unknown;\n",
		`                "application/json": components["schemas"]["Thing"];` + "\n",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("output is missing %q\n%s", want, got)
		}
	}
}

// Every construct outside the supported subset must fail rather than produce a
// type that silently disagrees with the contract.
func TestGenerateRejectsUnsupportedConstructs(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  []byte
		want string
	}{
		"unknown keyword": {
			document(`{"A": {"type": "object", "additionalProperties": false, "properties": {"x": {"type": "string", "nullable": true}}}}`),
			`keyword "nullable"`,
		},
		"oneOf": {
			document(`{"A": {"oneOf": [{"type": "string"}, {"type": "null"}]}}`),
			`keyword "oneOf"`,
		},
		"type array": {
			document(`{"A": {"type": ["string", "null"]}}`),
			`single string "type"`,
		},
		"open object": {
			document(`{"A": {"type": "object", "properties": {"x": {"type": "string"}}}}`),
			"must set additionalProperties",
		},
		"additionalProperties true": {
			document(`{"A": {"type": "object", "additionalProperties": true}}`),
			"additionalProperties: true",
		},
		"properties beside a map": {
			document(`{"A": {"type": "object", "additionalProperties": {"type": "string"}, "properties": {"x": {"type": "string"}}}}`),
			"properties next to an additionalProperties schema",
		},
		"undeclared required": {
			document(`{"A": {"type": "object", "additionalProperties": false, "required": ["y"], "properties": {"x": {"type": "string"}}}}`),
			"y is not a declared property",
		},
		"dangling ref": {
			document(`{"A": {"$ref": "#/components/schemas/Missing"}}`),
			"names no schema",
		},
		"external ref": {
			document(`{"A": {"$ref": "other.json#/A"}}`),
			"must point into",
		},
		"ref with siblings": {
			document(`{"A": {"$ref": "#/components/schemas/A", "description": "x"}}`),
			`"description" is not supported here`,
		},
		"enum literal of wrong type": {
			document(`{"A": {"type": "string", "enum": ["a", 1]}}`),
			"literal 1 is a number",
		},
		"array without items": {
			document(`{"A": {"type": "array"}}`),
			"A.items: expected an object",
		},
		"items on a string": {
			document(`{"A": {"type": "string", "items": {"type": "string"}}}`),
			`"items" does not apply to type string`,
		},
		"unknown components section": {
			[]byte(`{"components": {"parameters": {}}}`),
			"components.parameters is not supported",
		},
		"response without body": {
			[]byte(`{"components": {"responses": {"Gone": {"description": "x", "content": {}}}}}`),
			"without a body",
		},
		"response links": {
			[]byte(`{"components": {"responses": {"Gone": {"description": "x", "links": {}}}}}`),
			`"links" is not supported here`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Generate(tc.doc)
			if err == nil {
				t.Fatalf("Generate succeeded; want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}
