package skillregistry

import (
	"encoding/json"
	"strings"
	"testing"
)

func compileOrDie(t *testing.T, raw string) *compiledSchema {
	t.Helper()
	s, err := CompileSchema([]byte(raw))
	if err != nil {
		t.Fatalf("CompileSchema(%s): %v", raw, err)
	}
	return s
}

func TestCompileSchemaAcceptsTheSupportedSubset(t *testing.T) {
	// Every supported keyword, in one schema. If a future keyword is added
	// to the subset, add it here; if one is removed, remove it here.
	raw := `{
		"type": "object",
		"title": "step params",
		"description": "the closed subset",
		"properties": {
			"name":  {"type": "string", "minLength": 1, "maxLength": 64, "pattern": "^[a-z][a-z0-9-]*$"},
			"count": {"type": "integer", "minimum": 0, "maximum": 100},
			"ratio": {"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 1},
			"mode":  {"enum": ["fast", "slow"]},
			"tags":  {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 10, "uniqueItems": true},
			"nested": {"type": ["object", "null"], "properties": {"x": {"type": "boolean"}}, "additionalProperties": false}
		},
		"required": ["name", "mode"],
		"additionalProperties": false
	}`
	if _, err := CompileSchema([]byte(raw)); err != nil {
		t.Fatalf("a schema using only supported keywords was refused: %v", err)
	}
}

func TestCompileSchemaFailsClosedOnUnsupportedKeywords(t *testing.T) {
	// Every keyword outside the subset is refused, named. This is D6's rule:
	// unknown constructs fail closed rather than validating partially.
	// Every keyword outside the subset is refused, named. This is D6's rule:
	// unknown constructs fail closed rather than validating partially.
	// items-as-array rides along: the tuple form is not the single-schema form
	// the subset supports, and it is refused too (by a decode error, which is
	// still a refusal — the fail-closed requirement is that it is never
	// silently accepted).
	for kw, raw := range map[string]string{
		"$ref":                  `{"$ref": "#/definitions/x"}`,
		"$defs":                 `{"$defs": {}}`,
		"$schema":               `{"$schema": "https://json-schema.org/draft/2020-12/schema"}`,
		"allOf":                 `{"allOf": []}`,
		"anyOf":                 `{"anyOf": []}`,
		"oneOf":                 `{"oneOf": []}`,
		"not":                   `{"not": {}}`,
		"if":                    `{"if": {}, "then": {}}`,
		"format":                `{"type": "string", "format": "email"}`,
		"patternProperties":     `{"patternProperties": {}}`,
		"prefixItems":           `{"prefixItems": []}`,
		"contains":              `{"contains": {}}`,
		"multipleOf":            `{"type": "number", "multipleOf": 2}`,
		"dependencies":          `{"dependencies": {}}`,
		"const":                 `{"const": 1}`,
		"minProperties":         `{"minProperties": 1}`,
		"maxProperties":         `{"maxProperties": 1}`,
		"propertyNames":         `{"propertyNames": {}}`,
		"unevaluatedProperties": `{"unevaluatedProperties": false}`,
		"items-as-array":        `{"items": []}`,
		"unknown-whatever":      `{"whatever": 1}`,
	} {
		_, err := CompileSchema([]byte(raw))
		if err == nil {
			t.Errorf("schema keyword %q outside the subset was accepted", kw)
			continue
		}
		if kw == "items-as-array" {
			// The tuple form is refused by the decode, not by the unknown-key
			// gate, so its message names the type mismatch instead of the
			// supported subset. It is still a refusal — that is what fail-closed
			// requires — and the block just above already covers the message
			// shape for the keyword-shaped refusals.
			continue
		}
		// The refusal must name the offending construct and point at the
		// supported set, so a caller can self-correct.
		if !strings.Contains(err.Error(), "supported") {
			t.Errorf("refusal for %q should point at the supported subset; got %v", kw, err)
		}
	}
}

func TestCompileSchemaRejectsMalformedUses(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown type":               `{"type": "file"}`,
		"empty type array":           `{"type": []}`,
		"non-string in type":         `{"type": ["string", 3]}`,
		"type not a string":          `{"type": {"a": 1}}`,
		"invalid pattern":            `{"type": "string", "pattern": "["}`,
		"required not in properties": `{"type": "object", "required": ["x"], "properties": {"y": {"type": "string"}}}`,
		"required duplicate":         `{"type": "object", "required": ["x", "x"], "properties": {"x": {"type": "string"}}}`,
		"required empty name":        `{"type": "object", "required": [""]}`,
		"negative minLength":         `{"type": "string", "minLength": -1}`,
		"min > max length":           `{"type": "string", "minLength": 5, "maxLength": 2}`,
		"min > max items":            `{"type": "array", "minItems": 5, "maxItems": 2}`,
		"min > max value":            `{"type": "number", "minimum": 5, "maximum": 2}`,
		"empty enum":                 `{"enum": []}`,
		"duplicate enum member":      `{"enum": [1, 1]}`,
		"not an object":              `3`,
		"empty schema":               ``,
	} {
		if _, err := CompileSchema([]byte(raw)); err == nil {
			t.Errorf("%s: CompileSchema accepted a malformed schema", name)
		}
	}
}

func TestCheckValueEnforcesEverySupportedKeyword(t *testing.T) {
	s := compileOrDie(t, `{
		"type": "object",
		"properties": {
			"name":  {"type": "string", "minLength": 2, "maxLength": 4, "pattern": "^[a-z]+$"},
			"count": {"type": "integer", "minimum": 1, "maximum": 3},
			"ratio": {"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 1},
			"mode":  {"enum": ["fast", "slow"]},
			"tags":  {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 2, "uniqueItems": true},
			"flag":  {"type": "boolean"},
			"opt":   {"type": ["string", "null"]}
		},
		"required": ["name"],
		"additionalProperties": false
	}`)

	for raw, wantErr := range map[string]any{
		`{"name":"ab"}`:                      nil, // the happy path
		`{}`:                                 "required",
		`{"name":"a"}`:                       "minLength",
		`{"name":"abcde"}`:                   "maxLength",
		`{"name":"AB"}`:                      "pattern",
		`{"name":3}`:                         "type",
		`{"name":"ab","extra":1}`:            "additionalProperties",
		`{"name":"ab","count":0}`:            "minimum",
		`{"name":"ab","count":4}`:            "maximum",
		`{"name":"ab","count":2.5}`:          "type",
		`{"name":"ab","count":2.0}`:          nil, // 2.0 IS an integer per JSON Schema
		`{"name":"ab","ratio":0}`:            "exclusiveMinimum",
		`{"name":"ab","ratio":1}`:            "exclusiveMaximum",
		`{"name":"ab","ratio":0.5}`:          nil,
		`{"name":"ab","mode":"medium"}`:      "enum",
		`{"name":"ab","mode":"fast"}`:        nil,
		`{"name":"ab","tags":[]}`:            "minItems",
		`{"name":"ab","tags":["a","b","c"]}`: "maxItems",
		`{"name":"ab","tags":["a","a"]}`:     "uniqueItems",
		`{"name":"ab","tags":["a",3]}`:       "type",
		`{"name":"ab","tags":["a"]}`:         nil,
		`{"name":"ab","flag":"yes"}`:         "type",
		`{"name":"ab","flag":true}`:          nil,
		`{"name":"ab","opt":null}`:           nil,
		`{"name":"ab","opt":5}`:              "type",
	} {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		err := s.CheckValue(v)
		if wantErr == nil {
			if err != nil {
				t.Errorf("CheckValue(%s) = %v, want nil", raw, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("CheckValue(%s) = nil, want an error about %q", raw, wantErr)
			continue
		}
		if !strings.Contains(err.Error(), wantErr.(string)) {
			t.Errorf("CheckValue(%s) error %q does not mention %q", raw, err, wantErr)
		}
	}
}

func TestCheckValuePathsNameWhereItFailed(t *testing.T) {
	s := compileOrDie(t, `{
		"type": "object",
		"properties": {"list": {"type": "array", "items": {"type": "string"}}}
	}`)
	var v any
	if err := json.Unmarshal([]byte(`{"list":[ "ok", 3 ]}`), &v); err != nil {
		t.Fatal(err)
	}
	err := s.CheckValue(v)
	if err == nil {
		t.Fatal("the bad element was accepted")
	}
	if !strings.Contains(err.Error(), "$.list[1]") {
		t.Errorf("the violation should be named at $.list[1]; got %v", err)
	}
}

func TestCompileSchemaRejectsIncorrectKeyCaseRecursively(t *testing.T) {
	for name, raw := range map[string]string{
		"root key":   `{"Type":"string"}`,
		"nested key": `{"type":"object","properties":{"x":{"MinLength":1}}}`,
	} {
		if _, err := CompileSchema([]byte(raw)); err == nil {
			t.Errorf("%s: CompileSchema accepted an incorrectly cased key", name)
		} else if !strings.Contains(err.Error(), "incorrect case") {
			t.Errorf("%s: refusal should name the exact-key requirement; got %v", name, err)
		}
	}
}

// TestCompileSchemaRejectsNullEverywhere pins the fail-closed rule against
// the one JSON shape the struct decode silently accepts: null. A null schema
// node and an explicitly null keyword each decoded as "no constraint at all",
// which is precisely the silently-unconstrained schema the review forbids.
func TestCompileSchemaRejectsNullEverywhere(t *testing.T) {
	for name, raw := range map[string]string{
		"null root":                 `null`,
		"null root with whitespace": " null ",
		"nested null property":      `{"type":"object","properties":{"x":null}}`,
		"deep nested null property": `{"properties":{"a":{"properties":{"b":null}}}}`,
		"null items":                `{"type":"array","items":null}`,
		"type null":                 `{"type":null}`,
		"properties null":           `{"properties":null}`,
		"required null":             `{"required":null}`,
		"additionalProperties null": `{"additionalProperties":null}`,
		"items null":                `{"items":null}`,
		"minItems null":             `{"minItems":null}`,
		"maxItems null":             `{"maxItems":null}`,
		"uniqueItems null":          `{"uniqueItems":null}`,
		"minLength null":            `{"minLength":null}`,
		"maxLength null":            `{"maxLength":null}`,
		"pattern null":              `{"pattern":null}`,
		"minimum null":              `{"minimum":null}`,
		"maximum null":              `{"maximum":null}`,
		"exclusiveMinimum null":     `{"exclusiveMinimum":null}`,
		"exclusiveMaximum null":     `{"exclusiveMaximum":null}`,
		"enum null":                 `{"enum":null}`,
		"title null":                `{"title":null}`,
		"description null":          `{"description":null}`,
	} {
		if _, err := CompileSchema([]byte(raw)); err == nil {
			t.Errorf("%s: CompileSchema accepted a null where a constraint or an omission was required", name)
		} else if !strings.Contains(err.Error(), "null") {
			t.Errorf("%s: refusal should name null as the reason; got %v", name, err)
		}
	}
}

// TestCompileSchemaRejectsDuplicateKeysRecursively: a document whose meaning
// depends on which of two identical keys wins is ambiguous, and an ambiguous
// document is not a schema. Go and PostgreSQL jsonb both keep the LAST copy —
// agreeing only by accident of implementation, which is why the refusal
// exists rather than a "pick one" rule.
func TestCompileSchemaRejectsDuplicateKeysRecursively(t *testing.T) {
	for name, raw := range map[string]string{
		"top level":          `{"type":"string","type":"number"}`,
		"nested property":    `{"properties":{"a":{"minLength":1,"minLength":2}}}`,
		"via escape":         `{"type":"string","\u0074ype":"number"}`,
		"required key twice": `{"type":"object","required":["a"],"required":["b"]}`,
	} {
		if _, err := CompileSchema([]byte(raw)); err == nil {
			t.Errorf("%s: CompileSchema accepted duplicate keys: %s", name, raw)
		} else if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("%s: refusal should name the duplicate; got %v", name, err)
		}
	}
}

// TestCompileSchemaRejectsTrailingTextAfterTheDocument: exactly one value, and
// the tail must be well-formed nothing. A trailing syntax error used to read
// as success because only err == nil was treated as "trailing content".
func TestCompileSchemaRejectsTrailingTextAfterTheDocument(t *testing.T) {
	for name, raw := range map[string]string{
		"second document":  `{"type":"string"}{"type":"number"}`,
		"garbage tail":     `{"type":"string"} garbage`,
		"partial tail":     `{"type":"string"}}`,
		"nul byte tail":    "{\"type\":\"string\"}\x00",
		"junk after array": `{"type":"array","items":{"type":"string"}} ]`,
	} {
		if _, err := CompileSchema([]byte(raw)); err == nil {
			t.Errorf("%s: CompileSchema accepted trailing text: %q", name, raw)
		}
	}
}

// ─── aihub#708 Batch 1A repair: enum/uniqueItems semantic comparison ────────

// TestCompileSchemaEnumRejectsSemanticDuplicates: enum members are JSON
// VALUES, not spellings. 1 and 1.0 are one member written twice, an object
// and its key-reordering are one member written twice — while two numbers
// that only differ beyond float64 precision are genuinely distinct members
// (the dedup is exact decimal, never float).
func TestCompileSchemaEnumRejectsSemanticDuplicates(t *testing.T) {
	dupes := map[string]string{
		"1 vs 1.0":            `{"enum":[1,1.0]}`,
		"1 vs 1e0":            `{"enum":[1,1e0]}`,
		"0 vs -0":             `{"enum":[0,-0]}`,
		"2.50 vs 2.5":         `{"enum":[2.50,2.5]}`,
		"reordered object":    `{"enum":[{"a":1,"b":2},{"b":2,"a":1}]}`,
		"reordered deep":      `{"enum":[{"x":{"a":1,"b":2}},{"x":{"b":2,"a":1}}]}`,
		"escaped string same": `{"enum":["a","\u0061"]}`,
	}
	for name, raw := range dupes {
		if _, err := CompileSchema([]byte(raw)); err == nil {
			t.Errorf("%s: CompileSchema accepted a semantically duplicate enum: %s", name, raw)
		}
	}
	// Genuinely distinct members must stay acceptable — including numbers that
	// a float64 cannot tell apart (exact dedup must not collapse them) and
	// value/type distinctions JSON keeps ("1" vs 1, true vs 1).
	ok := map[string]string{
		"distinct big ints": `{"enum":[123456789012345678901234567890,123456789012345678901234567891]}`,
		"string vs number":  `{"enum":["1",1]}`,
		"bool vs number":    `{"enum":[true,1]}`,
		"null vs zero":      `{"enum":[null,0]}`,
		"array order":       `{"enum":[[1,2],[2,1]]}`,
		"different objects": `{"enum":[{"a":null},{"b":null}]}`,
	}
	for name, raw := range ok {
		if _, err := CompileSchema([]byte(raw)); err != nil {
			t.Errorf("%s: CompileSchema refused a legal enum: %s: %v", name, raw, err)
		}
	}
}

// TestCheckValueEnumMatchesValuesNotSpellings: at validation time the value is
// decoded, so matching must compare JSON values: 1 satisfies an enum written
// 1.0, and an object value satisfies an enum member written with different
// key order. {"a":null} and {"b":null} stay different objects.
func TestCheckValueEnumMatchesValuesNotSpellings(t *testing.T) {
	cases := []struct {
		schema  string
		value   string
		wantErr bool
	}{
		{`{"enum":[1.0]}`, `1`, false},
		{`{"enum":[1]}`, `1.0`, false},
		{`{"enum":[{"a":1,"b":2}]}`, `{"b":2,"a":1}`, false},
		{`{"enum":[{"b":null},{"a":null}]}`, `{"a":null}`, false},
		{`{"enum":[{"a":null},{"b":null}]}`, `{"c":null}`, true}, // key sets differ from BOTH members: a null value is not a missing key
		{`{"enum":["fast","slow"]}`, `"medium"`, true},
		// json.Unmarshal converts this exact integer into an unsafe float64;
		// callers that need it must decode with UseNumber (covered below).
		{`{"enum":[123456789012345678901234567890]}`, `123456789012345678901234567890`, true},
		{`{"enum":[1e2]}`, `100`, false},
	}
	for _, c := range cases {
		s := compileOrDie(t, c.schema)
		var v any
		if err := json.Unmarshal([]byte(c.value), &v); err != nil {
			t.Fatalf("unmarshal %s: %v", c.value, err)
		}
		err := s.CheckValue(v)
		if c.wantErr && err == nil {
			t.Errorf("CheckValue(%s) against %s: want an enum mismatch, got nil", c.value, c.schema)
		}
		if !c.wantErr && err != nil {
			t.Errorf("CheckValue(%s) against %s: value/spelling mismatch survived: %v", c.value, c.schema, err)
		}
	}
}

// TestCheckValueUniqueItemsComparesValues: [1, 1.0] and reordered objects are
// duplicate items; -0 and 0 are one number.
func TestCheckValueUniqueItemsComparesValues(t *testing.T) {
	s := compileOrDie(t, `{"type":"array","uniqueItems":true}`)
	for _, raw := range []string{`[1,1.0]`, `[0,-0]`, `[{"a":1,"b":2},{"b":2,"a":1}]`, `[2.50,2.5]`} {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if err := s.CheckValue(v); err == nil {
			t.Errorf("CheckValue(%s): semantically duplicate items passed uniqueItems", raw)
		}
	}
	// Negative zero folds recursively as well: the two objects are one JSON
	// value even though the signed float sits below the array item.
	nested := compileOrDie(t, `{"type":"object","properties":{"values":{"type":"array","uniqueItems":true}}}`)
	var nestedValue any
	if err := json.Unmarshal([]byte(`{"values":[{"n":-0},{"n":0}]}`), &nestedValue); err != nil {
		t.Fatalf("unmarshal nested negative zero: %v", err)
	}
	if err := nested.CheckValue(nestedValue); err == nil {
		t.Error("nested -0 and 0 passed uniqueItems")
	}

	for _, raw := range []string{`[1,2]`, `[{"a":1},{"a":2}]`, `[1,"1"]`} {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if err := s.CheckValue(v); err != nil {
			t.Errorf("CheckValue(%s): distinct items refused: %v", raw, err)
		}
	}
}

// TestCheckValuePreservesUseNumberPrecisionAndRejectsUnsafeFloat verifies the
// boundary contract: UseNumber keeps adjacent integers distinct, while an
// ordinary float64 decode is rejected rather than silently validating a value
// whose original integer cannot be reconstructed.
func TestCheckValuePreservesUseNumberPrecisionAndRejectsUnsafeFloat(t *testing.T) {
	s := compileOrDie(t, `{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)

	dec := json.NewDecoder(strings.NewReader(`{"n":9007199254740993}`))
	dec.UseNumber()
	var exact any
	if err := dec.Decode(&exact); err != nil {
		t.Fatalf("UseNumber decode: %v", err)
	}
	if err := s.CheckValue(exact); err != nil {
		t.Fatalf("UseNumber value was rejected: %v", err)
	}

	var rounded any
	if err := json.Unmarshal([]byte(`{"n":9007199254740992}`), &rounded); err != nil {
		t.Fatalf("float decode: %v", err)
	}
	if err := s.CheckValue(rounded); err == nil {
		t.Error("ordinary float64 decode of adjacent unsafe integer was accepted")
	} else if !strings.Contains(err.Error(), "unsafe float integer") {
		t.Errorf("unsafe float refusal should name the cause; got %v", err)
	}
}

func TestCheckValueRejectsUnsafeFloatThroughoutRuntimeValue(t *testing.T) {
	// Neither schema descends into the number: the first is enum-only and the
	// second deliberately omits items. The runtime guard must still reach it.
	for _, tc := range []struct {
		name, schema, value, wantPath string
	}{
		{
			name:     "nested enum-only object",
			schema:   `{"enum":[{"payload":{"n":0}}]}`,
			value:    `{"payload":{"n":9007199254740992}}`,
			wantPath: "$.payload.n",
		},
		{
			name:     "array without items",
			schema:   `{"type":"array"}`,
			value:    `[{"n":9007199254740992}]`,
			wantPath: "$[0].n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.value), &value); err != nil {
				t.Fatalf("float decode: %v", err)
			}
			err := compileOrDie(t, tc.schema).CheckValue(value)
			if err == nil || !strings.Contains(err.Error(), "unsafe float integer") {
				t.Fatalf("CheckValue() = %v, want unsafe float refusal", err)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Errorf("unsafe float refusal should name %s; got %v", tc.wantPath, err)
			}
		})
	}
}

func TestCheckValueKeepsNestedAdjacentBigIntsDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, schema, value string
	}{
		{
			name:   "nested object",
			schema: `{"enum":[{"n":9007199254740993}]}`,
			value:  `{"n":9007199254740994}`,
		},
		{
			name:   "array element",
			schema: `{"enum":[[9007199254740993]]}`,
			value:  `[9007199254740994]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec := json.NewDecoder(strings.NewReader(tc.value))
			dec.UseNumber()
			var value any
			if err := dec.Decode(&value); err != nil {
				t.Fatalf("UseNumber decode: %v", err)
			}
			if err := compileOrDie(t, tc.schema).CheckValue(value); err == nil {
				t.Error("adjacent exact integers matched the enum")
			} else if !strings.Contains(err.Error(), "enum") {
				t.Errorf("CheckValue() = %v, want enum mismatch", err)
			}
		})
	}
}
