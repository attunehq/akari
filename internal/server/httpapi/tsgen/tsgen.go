// Package tsgen turns the browser API's OpenAPI document into the TypeScript
// declarations the frontend imports (frontend/src/api.generated.ts).
//
// It emits TypeScript as plain text, so it never depends on a TypeScript
// compiler or its JavaScript API. The output keeps the shape the frontend reads:
// one exported `components` interface with `schemas` and `responses` members,
// indexed as components["schemas"]["Viewer"].
//
// It deliberately understands only the OpenAPI subset the document uses, and it
// returns an error for anything else. A keyword it does not know could change
// the meaning of a type, so guessing would let the frontend types drift from
// the contract without anyone noticing. Teach the generator the new construct
// (and its TypeScript meaning) instead of loosening the check.
package tsgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Header opens every generated file.
const Header = `/**
 * Generated from internal/server/httpapi/openapi.json by
 * internal/server/httpapi/tsgen. Do not edit by hand; run
 * ` + "`go generate ./internal/server/httpapi`" + ` instead.
 */
`

const indentUnit = "    "

// validationKeywords constrain a value without changing its TypeScript type,
// so the generator accepts and ignores them.
var validationKeywords = map[string]bool{
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"multipleOf": true, "minLength": true, "maxLength": true, "pattern": true,
	"minItems": true, "maxItems": true, "uniqueItems": true,
}

// docKeywords only feed the JSDoc comment above a property.
var docKeywords = map[string]bool{"description": true, "format": true, "default": true}

var identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

const schemaRefPrefix = "#/components/schemas/"

// Generate renders the TypeScript declarations for an OpenAPI document.
func Generate(document []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(document))
	dec.UseNumber() // keep numeric literals exactly as written
	var doc struct {
		Components map[string]json.RawMessage `json:"components"`
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode OpenAPI document: %w", err)
	}
	var schemas map[string]any
	var responses map[string]any
	for key, raw := range doc.Components {
		switch key {
		case "schemas":
			if err := decode(raw, &schemas); err != nil {
				return nil, fmt.Errorf("components.schemas: %w", err)
			}
		case "responses":
			if err := decode(raw, &responses); err != nil {
				return nil, fmt.Errorf("components.responses: %w", err)
			}
		case "securitySchemes":
			// Describes authentication, not payload shapes; nothing to emit.
		default:
			return nil, fmt.Errorf("components.%s is not supported by tsgen", key)
		}
	}

	g := &generator{schemas: schemas}
	var out strings.Builder
	out.WriteString(Header)
	out.WriteString("\nexport interface components {\n")

	out.WriteString(indentUnit + "schemas: {\n")
	for _, name := range sortedKeys(schemas) {
		at := "components.schemas." + name
		schema, err := asObject(schemas[name], at)
		if err != nil {
			return nil, err
		}
		if err := g.member(&out, 2, name, schema, true, at); err != nil {
			return nil, err
		}
	}
	out.WriteString(indentUnit + "};\n")

	out.WriteString(indentUnit + "responses: {\n")
	for _, name := range sortedKeys(responses) {
		if err := g.response(&out, name, responses[name]); err != nil {
			return nil, err
		}
	}
	out.WriteString(indentUnit + "};\n")
	out.WriteString("}\n")
	return []byte(out.String()), nil
}

type generator struct {
	schemas map[string]any
}

// response renders one components.responses entry as
// { headers: {...}; content: { "<media type>": T } }, the shape
// openapi-typescript used, so existing indexing keeps working.
func (g *generator) response(out *strings.Builder, name string, value any) error {
	at := "components.responses." + name
	response, err := asObject(value, at)
	if err != nil {
		return err
	}
	if err := allowKeys(response, at, "description", "content", "headers"); err != nil {
		return err
	}
	content, err := asObject(response["content"], at+".content")
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return fmt.Errorf("%s.content: a response without a body is not supported by tsgen", at)
	}

	writeDoc(out, 2, docLines(response))
	fmt.Fprintf(out, "%s%s: {\n", indent(2), propertyName(name))

	fmt.Fprintf(out, "%sheaders: {\n", indent(3))
	if headers, ok := response["headers"]; ok {
		headers, err := asObject(headers, at+".headers")
		if err != nil {
			return err
		}
		for _, header := range sortedKeys(headers) {
			hat := at + ".headers." + header
			h, err := asObject(headers[header], hat)
			if err != nil {
				return err
			}
			if err := allowKeys(h, hat, "description", "required", "schema"); err != nil {
				return err
			}
			required, err := optionalBool(h, "required", hat)
			if err != nil {
				return err
			}
			schema, err := asObject(h["schema"], hat+".schema")
			if err != nil {
				return err
			}
			typ, err := g.typeExpr(schema, 4, hat+".schema")
			if err != nil {
				return err
			}
			writeDoc(out, 4, docLines(h))
			fmt.Fprintf(out, "%s%s%s: %s;\n", indent(4), propertyName(header), optionalMark(!required), typ)
		}
	}
	fmt.Fprintf(out, "%s[name: string]: unknown;\n", indent(4))
	fmt.Fprintf(out, "%s};\n", indent(3))

	fmt.Fprintf(out, "%scontent: {\n", indent(3))
	for _, mediaType := range sortedKeys(content) {
		mat := at + ".content." + mediaType
		media, err := asObject(content[mediaType], mat)
		if err != nil {
			return err
		}
		if err := allowKeys(media, mat, "schema"); err != nil {
			return err
		}
		schema, err := asObject(media["schema"], mat+".schema")
		if err != nil {
			return err
		}
		if err := g.member(out, 4, mediaType, schema, true, mat+".schema"); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%s};\n", indent(3))
	fmt.Fprintf(out, "%s};\n", indent(2))
	return nil
}

// member writes `name?: T;` at the given depth, preceded by its JSDoc.
func (g *generator) member(out *strings.Builder, depth int, name string, schema map[string]any, required bool, at string) error {
	typ, err := g.typeExpr(schema, depth, at)
	if err != nil {
		return err
	}
	writeDoc(out, depth, docLines(schema))
	fmt.Fprintf(out, "%s%s%s: %s;\n", indent(depth), propertyName(name), optionalMark(!required), typ)
	return nil
}

// typeExpr renders a schema as a TypeScript type expression. depth is the
// indentation level of the line the expression starts on, so multi-line object
// types close at the same depth.
func (g *generator) typeExpr(schema map[string]any, depth int, at string) (string, error) {
	for _, key := range sortedKeys(schema) {
		switch {
		case validationKeywords[key], docKeywords[key]:
		case key == "type", key == "$ref", key == "anyOf", key == "enum", key == "const",
			key == "properties", key == "required", key == "additionalProperties", key == "items":
		default:
			return "", fmt.Errorf("%s: keyword %q is not supported by tsgen", at, key)
		}
	}
	if len(schema) == 0 {
		return "unknown", nil // the empty schema accepts any value
	}

	if ref, ok := schema["$ref"]; ok {
		if err := allowKeys(schema, at, "$ref"); err != nil {
			return "", err
		}
		s, ok := ref.(string)
		name, found := strings.CutPrefix(s, schemaRefPrefix)
		if !ok || !found {
			return "", fmt.Errorf("%s: $ref %v must point into %s", at, ref, schemaRefPrefix)
		}
		if _, ok := g.schemas[name]; !ok {
			return "", fmt.Errorf("%s: $ref %s names no schema", at, s)
		}
		return fmt.Sprintf("components[\"schemas\"][%s]", literal(name)), nil
	}

	if variants, ok := schema["anyOf"]; ok {
		if err := allowKeys(schema, at, "anyOf", "description"); err != nil {
			return "", err
		}
		list, ok := variants.([]any)
		if !ok || len(list) == 0 {
			return "", fmt.Errorf("%s.anyOf: must be a non-empty array", at)
		}
		parts := make([]string, len(list))
		for i, variant := range list {
			vat := fmt.Sprintf("%s.anyOf[%d]", at, i)
			v, err := asObject(variant, vat)
			if err != nil {
				return "", err
			}
			if parts[i], err = g.typeExpr(v, depth, vat); err != nil {
				return "", err
			}
		}
		return strings.Join(parts, " | "), nil
	}

	typ, ok := schema["type"].(string)
	if !ok {
		return "", fmt.Errorf("%s: schema needs a single string \"type\", a \"$ref\", or \"anyOf\"", at)
	}
	switch typ {
	case "object":
		if err := forbidKeys(schema, at, typ, "items", "enum", "const"); err != nil {
			return "", err
		}
		return g.objectExpr(schema, depth, at)
	case "array":
		if err := forbidKeys(schema, at, typ, "properties", "required", "additionalProperties", "enum", "const"); err != nil {
			return "", err
		}
		items, err := asObject(schema["items"], at+".items")
		if err != nil {
			return "", err
		}
		elem, err := g.typeExpr(items, depth, at+".items")
		if err != nil {
			return "", err
		}
		if isUnion(items) {
			elem = "(" + elem + ")"
		}
		return elem + "[]", nil
	case "string", "integer", "number", "boolean", "null":
		if err := forbidKeys(schema, at, typ, "properties", "required", "additionalProperties", "items"); err != nil {
			return "", err
		}
		return scalarExpr(schema, typ, at)
	default:
		return "", fmt.Errorf("%s: type %q is not supported by tsgen", at, typ)
	}
}

// isUnion reports whether a schema renders as a top-level union, which needs
// parentheses before an array's [] suffix.
func isUnion(schema map[string]any) bool {
	if variants, ok := schema["anyOf"].([]any); ok {
		if len(variants) == 1 {
			variant, _ := variants[0].(map[string]any)
			return isUnion(variant)
		}
		return true
	}
	values, _ := schema["enum"].([]any)
	return len(values) > 1
}

// objectExpr renders a closed object (additionalProperties: false) or a string
// map (additionalProperties: <schema>, no properties). An object must say which
// it is: JSON Schema's default is open, which has no faithful TypeScript form
// next to named properties.
func (g *generator) objectExpr(schema map[string]any, depth int, at string) (string, error) {
	properties := map[string]any{}
	if raw, ok := schema["properties"]; ok {
		var err error
		if properties, err = asObject(raw, at+".properties"); err != nil {
			return "", err
		}
	}
	required := map[string]bool{}
	if raw, ok := schema["required"]; ok {
		list, ok := raw.([]any)
		if !ok {
			return "", fmt.Errorf("%s.required: must be an array of property names", at)
		}
		for _, item := range list {
			name, ok := item.(string)
			if _, declared := properties[name]; !ok || !declared {
				return "", fmt.Errorf("%s.required: %v is not a declared property", at, item)
			}
			required[name] = true
		}
	}

	switch extra := schema["additionalProperties"].(type) {
	case bool:
		if extra {
			return "", fmt.Errorf("%s: additionalProperties: true is not supported by tsgen; give the value a schema", at)
		}
		if len(properties) == 0 {
			return "Record<string, never>", nil
		}
		var body strings.Builder
		body.WriteString("{\n")
		for _, name := range sortedKeys(properties) {
			pat := at + ".properties." + name
			property, err := asObject(properties[name], pat)
			if err != nil {
				return "", err
			}
			// openapi-typescript's defaultNonNullable rule, kept so the types
			// did not change when it was replaced: a property with a default is
			// treated as always present.
			_, hasDefault := property["default"]
			if err := g.member(&body, depth+1, name, property, required[name] || hasDefault, pat); err != nil {
				return "", err
			}
		}
		body.WriteString(indent(depth) + "}")
		return body.String(), nil
	case map[string]any:
		if len(properties) > 0 {
			return "", fmt.Errorf("%s: properties next to an additionalProperties schema are not supported by tsgen", at)
		}
		value, err := g.typeExpr(extra, depth+1, at+".additionalProperties")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("{\n%s[key: string]: %s;\n%s}", indent(depth+1), value, indent(depth)), nil
	case nil:
		return "", fmt.Errorf("%s: object schemas must set additionalProperties (false, or a value schema)", at)
	default:
		return "", fmt.Errorf("%s.additionalProperties: must be false or a schema", at)
	}
}

// scalarExpr renders a primitive type, narrowed to literals by enum or const.
func scalarExpr(schema map[string]any, typ, at string) (string, error) {
	base := map[string]string{
		"string": "string", "integer": "number", "number": "number", "boolean": "boolean", "null": "null",
	}[typ]
	var values []any
	if raw, ok := schema["enum"]; ok {
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			return "", fmt.Errorf("%s.enum: must be a non-empty array", at)
		}
		values = list
	}
	if raw, ok := schema["const"]; ok {
		if values != nil {
			return "", fmt.Errorf("%s: enum and const together are not supported by tsgen", at)
		}
		values = []any{raw}
	}
	if values == nil {
		return base, nil
	}
	parts := make([]string, len(values))
	for i, value := range values {
		if kind := jsonKind(value); kind != base {
			return "", fmt.Errorf("%s: literal %v is a %s, but the schema's type is %s", at, value, kind, typ)
		}
		parts[i] = literal(value)
	}
	return strings.Join(parts, " | "), nil
}

func jsonKind(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "a composite value"
	}
}

// docLines collects a schema's JSDoc: its description, format, and default.
func docLines(schema map[string]any) []string {
	var lines []string
	if description, ok := schema["description"].(string); ok && description != "" {
		lines = append(lines, strings.Split(description, "\n")...)
	}
	if format, ok := schema["format"].(string); ok {
		lines = append(lines, "Format: "+format)
	}
	if value, ok := schema["default"]; ok {
		lines = append(lines, "@default "+literal(value))
	}
	return lines
}

func writeDoc(out *strings.Builder, depth int, lines []string) {
	for i, line := range lines {
		// A literal "*/" would end the comment early.
		lines[i] = strings.ReplaceAll(line, "*/", "*\\/")
	}
	switch len(lines) {
	case 0:
	case 1:
		fmt.Fprintf(out, "%s/** %s */\n", indent(depth), lines[0])
	default:
		fmt.Fprintf(out, "%s/**\n", indent(depth))
		for _, line := range lines {
			fmt.Fprintf(out, "%s * %s\n", indent(depth), line)
		}
		fmt.Fprintf(out, "%s */\n", indent(depth))
	}
}

// literal renders a JSON scalar as TypeScript. JSON string and number syntax
// is valid TypeScript literal syntax.
func literal(value any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value) // scalars from a decoded document always encode
	return strings.TrimSuffix(buf.String(), "\n")
}

func propertyName(name string) string {
	if identifier.MatchString(name) {
		return name
	}
	return literal(name)
}

func optionalMark(optional bool) string {
	if optional {
		return "?"
	}
	return ""
}

func indent(depth int) string { return strings.Repeat(indentUnit, depth) }

func decode(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(into)
}

func asObject(value any, at string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected an object", at)
	}
	return object, nil
}

func optionalBool(object map[string]any, key, at string) (bool, error) {
	raw, ok := object[key]
	if !ok {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s.%s: expected a boolean", at, key)
	}
	return value, nil
}

func allowKeys(object map[string]any, at string, allowed ...string) error {
	for _, key := range sortedKeys(object) {
		if !slices.Contains(allowed, key) {
			return fmt.Errorf("%s: %q is not supported here by tsgen", at, key)
		}
	}
	return nil
}

func forbidKeys(schema map[string]any, at, typ string, forbidden ...string) error {
	for _, key := range forbidden {
		if _, ok := schema[key]; ok {
			return fmt.Errorf("%s: %q does not apply to type %s", at, key, typ)
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
