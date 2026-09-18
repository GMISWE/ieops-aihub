package skillregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// ─── The fail-closed JSON Schema subset (spec D6) ────────────────────────────
//
// Contracts may carry input/params/output schemas. The rule is D6's: "Schemas
// must use a supported validated subset; unknown constructs fail closed
// rather than pretending a partial validator implements JSON Schema."
//
// The SUPPORTED SUBSET is exactly these keywords (the "happy subset" every
// caller needs for typed params/inputs/outputs):
//
//	type, properties, required, additionalProperties,
//	items, minItems, maxItems, uniqueItems,
//	minLength, maxLength, pattern,
//	minimum, maximum, exclusiveMinimum, exclusiveMaximum,
//	enum,
//	title, description                (metadata: parsed, validated, ignored)
//
// NOTHING ELSE. No $ref, no $defs, no allOf/anyOf/oneOf/not, no
// format, no patternProperties, no prefixItems, no if/then/else, no
// $schema/$id. Each of those is refused with the keyword named — a caller
// cannot get a silent partial validation by using one.
//
// The subset is enforced at COMPILE time (CompileSchema), not at validation
// time, and CompileSchema runs at publish, so a stored contract's schemas are
// all inside the subset BY CONSTRUCTION and the executor (later batches) can
// validate values without re-auditing the schema.

// schemaDoc is the strict parse of one schema object. Every field the subset
// supports appears here; DisallowUnknownFields refuses everything else.
// json.RawMessage (not []byte) is load-bearing on the nested fields:
// encoding/json special-cases RawMessage to store verbatim bytes, while a
// plain []byte would be decoded as a base64 STRING and every nested schema
// would fail with a confusing message.
type schemaDoc struct {
	Type                 any                        `json:"type,omitempty"` // string or []string
	Properties           map[string]json.RawMessage `json:"properties,omitempty"`
	Required             []string                   `json:"required,omitempty"`
	AdditionalProperties *bool                      `json:"additionalProperties,omitempty"`
	Items                *json.RawMessage           `json:"items,omitempty"` // single-schema form only
	MinItems             *int                       `json:"minItems,omitempty"`
	MaxItems             *int                       `json:"maxItems,omitempty"`
	UniqueItems          bool                       `json:"uniqueItems,omitempty"`
	MinLength            *int                       `json:"minLength,omitempty"`
	MaxLength            *int                       `json:"maxLength,omitempty"`
	Pattern              *string                    `json:"pattern,omitempty"`
	Minimum              json.RawMessage            `json:"minimum,omitempty"`
	Maximum              json.RawMessage            `json:"maximum,omitempty"`
	ExclusiveMinimum     json.RawMessage            `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     json.RawMessage            `json:"exclusiveMaximum,omitempty"`
	Enum                 []json.RawMessage          `json:"enum,omitempty"`
	Title                string                     `json:"title,omitempty"`
	Description          string                     `json:"description,omitempty"`
}

// compiledSchema is the validated, ready-to-validate form of one schema.
type compiledSchema struct {
	types                []string // "object"|"array"|"string"|"number"|"integer"|"boolean"; empty = any
	properties           map[string]*compiledSchema
	required             []string
	additionalProperties *bool
	items                *compiledSchema
	minItems, maxItems   *int
	uniqueItems          bool
	minLength, maxLength *int
	pattern              *regexp.Regexp
	minimum, maximum     *big.Rat
	exclusiveMinimum     *big.Rat
	exclusiveMaximum     *big.Rat
	enum                 []json.RawMessage
	// enumDecoded is each enum member decoded to a plain JSON value once at
	// compile time, so runtime enum matching compares JSON VALUES (1 == 1.0,
	// key order irrelevant) instead of raw spellings. See semanticJSONEqual.
	enumDecoded []any
}

// schemaTypeVocabulary is the closed set of JSON type names. "null" is
// included: params/inputs legitimately have optional fields, and refusing
// the type entirely would push authors to drop the type keyword and lose the
// checking it buys.
var schemaTypeVocabulary = map[string]bool{
	"object": true, "array": true, "string": true,
	"number": true, "integer": true, "boolean": true, "null": true,
}

// SupportedSchemaKeywords lists the keywords the subset implements, sorted.
// It is the answer CompileSchema and CheckValue error messages point to, and
// the one list tool descriptions should publish.
func SupportedSchemaKeywords() []string {
	return []string{
		"additionalProperties", "description", "enum", "exclusiveMaximum",
		"exclusiveMinimum", "items", "maxItems", "maxLength", "maximum",
		"minItems", "minLength", "minimum", "pattern", "properties",
		"required", "title", "type", "uniqueItems",
	}
}

// CompileSchema parses raw schema JSON into a validated compiledSchema. It
// fails closed: a JSON error, an unknown keyword, a type outside the
// vocabulary, an invalid pattern, an empty required/enum, or an unsupported
// use (e.g. items as an array) is an error naming the problem. The returned
// schema is immutable and safe for concurrent use.
func CompileSchema(raw []byte) (*compiledSchema, error) {
	if len(raw) == 0 {
		return nil, errors.New("schema is empty")
	}
	return compileSchema(raw, "")
}

// compileSchema compiles raw with path naming the location of this schema
// within the document, for error messages.
func compileSchema(raw []byte, path string) (*compiledSchema, error) {
	var doc schemaDoc
	// decodeStrictOne refuses duplicate keys, trailing content and unknown
	// keywords, and returns the CANONICAL bytes it decoded — the form the
	// explicit-null checks below read.
	canon, err := decodeStrictOne(raw, &doc, schemaWhat(path), SupportedSchemaKeywords())
	if err != nil {
		return nil, err
	}
	// A schema NODE must be an object. JSON null is the one root a struct
	// decode accepts silently (json.Unmarshal of "null" is a no-op), which
	// made a null schema node validate as "unconstrained" — exactly the
	// silent pass the fail-closed rule forbids. The same rule holds at every
	// nesting level: a null value under properties/items arrives here as
	// its own node.
	if string(canon) == "null" {
		return nil, fmt.Errorf("schema %s is null; a schema must be an object (omit the node or give it keywords — null is never an unconstrained pass)", schemaWhere(path))
	}
	// An EXPLICIT null keyword is refused the same way: "type": null, "items":
	// null and friends each decode as "key absent" in the struct, turning an
	// author's explicit null into a silently unconstrained schema. Only the
	// canonical bytes can tell "key absent" from "key present with value
	// null" — the struct decode cannot.
	present := make(map[string]json.RawMessage)
	if err := json.Unmarshal(canon, &present); err != nil {
		// canon is a canonical object; this cannot fail. Refuse anyway
		// rather than skip the check.
		return nil, fmt.Errorf("schema %s is not valid: %w", schemaWhat(path), err)
	}
	for _, kw := range SupportedSchemaKeywords() {
		if v, ok := present[kw]; ok && string(v) == "null" {
			return nil, fmt.Errorf("schema %s: keyword %q is null; omit the keyword when no constraint is intended — an explicit null keyword is never an unconstrained pass", schemaWhere(path), kw)
		}
	}
	out := &compiledSchema{
		required:    doc.Required,
		uniqueItems: doc.UniqueItems,
	}
	if doc.AdditionalProperties != nil {
		v := *doc.AdditionalProperties
		out.additionalProperties = &v
	}

	// type: string or array of strings, all from the closed vocabulary.
	if doc.Type != nil {
		switch t := doc.Type.(type) {
		case string:
			if !schemaTypeVocabulary[t] {
				return nil, fmt.Errorf("schema %s: unknown type %q (supported: object, array, string, number, integer, boolean)", schemaWhere(path), t)
			}
			out.types = []string{t}
		case []any:
			if len(t) == 0 {
				return nil, fmt.Errorf("schema %s: type array is empty", schemaWhere(path))
			}
			for _, e := range t {
				s, ok := e.(string)
				if !ok || !schemaTypeVocabulary[s] {
					return nil, fmt.Errorf("schema %s: type array has a non-vocabulary member %v", schemaWhere(path), e)
				}
				out.types = append(out.types, s)
			}
		default:
			return nil, fmt.Errorf("schema %s: type must be a string or an array of strings", schemaWhere(path))
		}
	}

	// properties: every value is itself a supported schema.
	if len(doc.Properties) > 0 {
		out.properties = make(map[string]*compiledSchema, len(doc.Properties))
		names := make([]string, 0, len(doc.Properties))
		for name := range doc.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child, err := compileSchema(doc.Properties[name], path+".properties["+name+"]")
			if err != nil {
				return nil, err
			}
			out.properties[name] = child
		}
	}

	// required: distinct names, and each must appear in properties when
	// properties is present (JSON Schema allows required without properties,
	// but then it constrains nothing — refuse it as an authoring error).
	if len(doc.Required) > 0 {
		seen := map[string]bool{}
		for _, name := range doc.Required {
			if name == "" {
				return nil, fmt.Errorf("schema %s: required contains an empty name", schemaWhere(path))
			}
			if seen[name] {
				return nil, fmt.Errorf("schema %s: required lists %q twice", schemaWhere(path), name)
			}
			seen[name] = true
			if out.properties != nil && out.properties[name] == nil {
				return nil, fmt.Errorf("schema %s: required lists %q but properties does not define it", schemaWhere(path), name)
			}
		}
	}

	// items: the single-schema form only. (An array value here — the draft
	// 2020-12 tuple form, or the older prefixItems — decodes into the struct
	// with a type error and is refused; the subset documents items as a
	// single schema.)
	if doc.Items != nil {
		child, err := compileSchema(*doc.Items, path+".items")
		if err != nil {
			return nil, err
		}
		out.items = child
	}

	for name, v := range map[string]*int{
		"minItems": doc.MinItems, "maxItems": doc.MaxItems,
		"minLength": doc.MinLength, "maxLength": doc.MaxLength,
	} {
		if v == nil {
			continue
		}
		if *v < 0 {
			return nil, fmt.Errorf("schema %s: %s must be >= 0", schemaWhere(path), name)
		}
		switch name {
		case "minItems":
			out.minItems = v
		case "maxItems":
			out.maxItems = v
		case "minLength":
			out.minLength = v
		case "maxLength":
			out.maxLength = v
		}
	}
	if out.minItems != nil && out.maxItems != nil && *out.minItems > *out.maxItems {
		return nil, fmt.Errorf("schema %s: minItems (%d) exceeds maxItems (%d)", schemaWhere(path), *out.minItems, *out.maxItems)
	}
	if out.minLength != nil && out.maxLength != nil && *out.minLength > *out.maxLength {
		return nil, fmt.Errorf("schema %s: minLength (%d) exceeds maxLength (%d)", schemaWhere(path), *out.minLength, *out.maxLength)
	}

	if doc.Pattern != nil {
		re, err := regexp.Compile(*doc.Pattern)
		if err != nil {
			return nil, fmt.Errorf("schema %s: pattern is not a valid regular expression: %w", schemaWhere(path), err)
		}
		out.pattern = re
	}

	for name, raw := range map[string]json.RawMessage{
		"minimum": doc.Minimum, "maximum": doc.Maximum,
		"exclusiveMinimum": doc.ExclusiveMinimum, "exclusiveMaximum": doc.ExclusiveMaximum,
	} {
		if len(raw) == 0 {
			continue
		}
		n, err := schemaNumber(raw)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %s must be an exact JSON number: %w", schemaWhere(path), name, err)
		}
		switch name {
		case "minimum":
			out.minimum = n
		case "maximum":
			out.maximum = n
		case "exclusiveMinimum":
			out.exclusiveMinimum = n
		case "exclusiveMaximum":
			out.exclusiveMaximum = n
		}
	}
	if out.minimum != nil && out.maximum != nil && out.minimum.Cmp(out.maximum) > 0 {
		return nil, fmt.Errorf("schema %s: minimum exceeds maximum", schemaWhere(path))
	}

	if len(doc.Enum) > 0 {
		out.enum = doc.Enum
		out.enumDecoded = make([]any, len(doc.Enum))
		seen := map[string]bool{}
		for i, e := range doc.Enum {
			// Duplicate detection runs on the CANONICAL text, not the raw
			// spelling: enum members are JSON VALUES, so 1 and 1.0 are the
			// same member written twice (a duplicate), and so are an object
			// and its key-reordering — while two numbers that differ only
			// beyond float64 precision stay DISTINCT members, because the
			// canonicalization is exact decimal, never float64.
			canonical, cerr := canonicalizeJSON(e)
			if cerr != nil {
				return nil, fmt.Errorf("schema %s: enum member %d does not canonicalize: %w", schemaWhere(path), i, cerr)
			}
			if seen[string(canonical)] {
				return nil, fmt.Errorf("schema %s: enum lists the same value twice (member %d repeats an earlier member)", schemaWhere(path), i)
			}
			seen[string(canonical)] = true
			var decoded any
			decoder := json.NewDecoder(bytes.NewReader(canonical))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err != nil {
				return nil, fmt.Errorf("schema %s: enum member %d does not decode: %w", schemaWhere(path), i, err)
			}
			out.enumDecoded[i] = decoded
		}
	} else if doc.Enum != nil {
		return nil, fmt.Errorf("schema %s: enum must list at least one value", schemaWhere(path))
	}

	return out, nil
}

// CheckValue validates a decoded JSON value (the output of json.Unmarshal into
// any) against a compiled schema, returning nil or the first violation with a
// path naming where. It is the executor-facing half of the subset: Batch 1B's
// flow validation and later step execution validate step params/inputs with
// exactly this, so a contract cannot promise a check the runtime skips.
func (s *compiledSchema) CheckValue(value any) error {
	// Check the complete runtime tree before schema constraints. Some schemas
	// deliberately have no child schema (for example enum-only objects and
	// arrays without items), but an unsafe float must never evade validation
	// merely because no constraint descends to its location.
	if err := rejectUnsafeFloat(value, "$"); err != nil {
		return err
	}
	return s.check(value, "$")
}

func (s *compiledSchema) check(value any, path string) error {
	if s == nil {
		return nil
	}
	if len(s.types) > 0 {
		if !typeMatches(s.types, value) {
			return fmt.Errorf("%s: value is %s, schema requires type %s",
				path, jsonTypeName(value), strings.Join(s.types, "|"))
		}
	}
	if len(s.enum) > 0 {
		// Runtime matching compares JSON VALUES against the compiled enum
		// members, not spellings: value 1 matches an enum member written
		// 1.0, and an object value matches a member whose keys were written
		// in a different order. semanticJSONEqual is the one equality here.
		matched := false
		for _, candidate := range s.enumDecoded {
			if semanticJSONEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value is not one of the schema's enum values", path)
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for _, name := range s.required {
			if _, ok := v[name]; !ok {
				return fmt.Errorf("%s: required property %q is missing", path, name)
			}
		}
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if s.properties != nil {
				if child, ok := s.properties[name]; ok {
					if err := child.check(v[name], path+"."+name); err != nil {
						return err
					}
					continue
				}
			}
			if s.additionalProperties != nil && !*s.additionalProperties {
				return fmt.Errorf("%s: property %q is not in the schema's properties and additionalProperties is false", path, name)
			}
		}
	case []any:
		if s.minItems != nil && len(v) < *s.minItems {
			return fmt.Errorf("%s: array has %d items, minItems is %d", path, len(v), *s.minItems)
		}
		if s.maxItems != nil && len(v) > *s.maxItems {
			return fmt.Errorf("%s: array has %d items, maxItems is %d", path, len(v), *s.maxItems)
		}
		if s.uniqueItems {
			seen := map[string]bool{}
			for i, e := range v {
				// The key is built from the DECODED value, so uniqueness is
				// semantic: 1 and 1.0 are one item, and so are two objects
				// spelled with different key order (json.Marshal sorts map
				// keys). Negative zero is folded onto 0 — JSON numbers have
				// no signed zero, but a decoded float64 can carry one.
				key, kerr := uniqueItemsKey(e)
				if kerr != nil {
					return fmt.Errorf("%s[%d]: value does not serialize: %w", path, i, kerr)
				}
				if seen[string(key)] {
					return fmt.Errorf("%s[%d]: duplicate item; uniqueItems is set", path, i)
				}
				seen[string(key)] = true
			}
		}
		if s.items != nil {
			for i, e := range v {
				if err := s.items.check(e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case string:
		runes := utf8.RuneCountInString(v)
		if s.minLength != nil && runes < *s.minLength {
			return fmt.Errorf("%s: string length %d is under minLength %d", path, runes, *s.minLength)
		}
		if s.maxLength != nil && runes > *s.maxLength {
			return fmt.Errorf("%s: string length %d is over maxLength %d", path, runes, *s.maxLength)
		}
		if s.pattern != nil && !s.pattern.MatchString(v) {
			return fmt.Errorf("%s: string does not match pattern %q", path, s.pattern.String())
		}
	case json.Number:
		n, err := exactRuntimeNumber(v)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if s.minimum != nil && n.Cmp(s.minimum) < 0 {
			return fmt.Errorf("%s: value is under minimum", path)
		}
		if s.maximum != nil && n.Cmp(s.maximum) > 0 {
			return fmt.Errorf("%s: value is over maximum", path)
		}
		if s.exclusiveMinimum != nil && n.Cmp(s.exclusiveMinimum) <= 0 {
			return fmt.Errorf("%s: value violates exclusiveMinimum", path)
		}
		if s.exclusiveMaximum != nil && n.Cmp(s.exclusiveMaximum) >= 0 {
			return fmt.Errorf("%s: value violates exclusiveMaximum", path)
		}
	case float64:
		n := new(big.Rat).SetFloat64(v)
		if s.minimum != nil && n.Cmp(s.minimum) < 0 {
			return fmt.Errorf("%s: value is under minimum", path)
		}
		if s.maximum != nil && n.Cmp(s.maximum) > 0 {
			return fmt.Errorf("%s: value is over maximum", path)
		}
		if s.exclusiveMinimum != nil && n.Cmp(s.exclusiveMinimum) <= 0 {
			return fmt.Errorf("%s: value violates exclusiveMinimum", path)
		}
		if s.exclusiveMaximum != nil && n.Cmp(s.exclusiveMaximum) >= 0 {
			return fmt.Errorf("%s: value violates exclusiveMaximum", path)
		}
	}
	return nil
}

func schemaNumber(raw []byte) (*big.Rat, error) {
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var ok bool
	n, ok = value.(json.Number)
	if !ok {
		return nil, errors.New("must be a number")
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return nil, errors.New("invalid decimal")
	}
	return r, nil
}

func exactRuntimeNumber(n json.Number) (*big.Rat, error) {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return nil, fmt.Errorf("invalid JSON number %q", n)
	}
	return r, nil
}

func rejectUnsafeFloat(value any, path string) error {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%s: non-finite float is not a JSON number", path)
		}
		if math.Trunc(v) == v && math.Abs(v) > 9007199254740991 {
			return fmt.Errorf("%s: unsafe float integer exceeds exact JSON range (2^53-1)", path)
		}
	case map[string]any:
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := rejectUnsafeFloat(v[name], path+"."+name); err != nil {
				return err
			}
		}
	case []any:
		for i, element := range v {
			if err := rejectUnsafeFloat(element, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeMatches(types []string, value any) bool {
	jsonType := jsonTypeName(value)
	for _, t := range types {
		if t == jsonType {
			return true
		}
		if t == "number" && jsonType == "integer" {
			return true
		}
	}
	return false
}

// jsonTypeName names the JSON type of a decoded value the way JSON Schema
// does: numbers that are integers get their own name so "integer" can match
// exactly, while "number" still matches both.
func jsonTypeName(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if math.Trunc(v) == v {
			return "integer"
		}
		return "number"
	case json.Number:
		if _, err := exactRuntimeNumber(v); err != nil {
			return "unknown"
		}
		if !strings.ContainsAny(string(v), ".eE") {
			return "integer"
		}
		r, _ := exactRuntimeNumber(v)
		if r.IsInt() {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

// canonicalJSON serializes value deterministically (sorted map keys), for
// uniqueness comparisons over DECODED values. This is the runtime-value half;
// exact-spelling comparisons (schema texts, digests) go through
// canonicalizeJSON instead, which preserves exact numbers rather than the
// float64 form a decoded value already carries.
func canonicalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

// uniqueItemsKey is the uniqueness key for one DECODED array element.
func uniqueItemsKey(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// Reuse the exact recursive canonicalizer so json.Number values nested in
	// objects/arrays compare by value (1 == 1.0) rather than by spelling.
	return canonicalizeJSON(raw)
}

// semanticJSONEqual reports whether two DECODED JSON values are the same JSON
// value: numbers by their decoded value (1 == 1.0 — they are one number once
// decoded; precision beyond float64 was already surrendered by the caller's
// decode, and compile-time enum dedup is where exactness is enforced),
// objects by key SET and per-key values (order never mattered), arrays in
// order. A key present with a null value is NOT equal to a missing key —
// {"a":null} and {"b":null} are different objects.
func semanticJSONEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			w, present := bv[k]
			if !present {
				return false
			}
			if !semanticJSONEqual(v, w) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !semanticJSONEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case json.Number:
		bv, ok := b.(json.Number)
		if ok {
			ar, ae := exactRuntimeNumber(av)
			br, be := exactRuntimeNumber(bv)
			return ae == nil && be == nil && ar.Cmp(br) == 0
		}
		if bv, ok := b.(float64); ok {
			ar, err := exactRuntimeNumber(av)
			return err == nil && new(big.Rat).SetFloat64(bv).Cmp(ar) == 0
		}
		return false
	case float64:
		if bv, ok := b.(float64); ok {
			return av == bv
		}
		if bv, ok := b.(json.Number); ok {
			br, err := exactRuntimeNumber(bv)
			return err == nil && new(big.Rat).SetFloat64(av).Cmp(br) == 0
		}
		return false
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case nil:
		return b == nil
	}
	return false
}

func schemaWhere(path string) string {
	if path == "" {
		return "at the root"
	}
	return "at " + path
}

func schemaWhat(path string) string {
	if path == "" {
		return "schema"
	}
	return "schema " + path
}
