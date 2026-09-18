package skillregistry

// canonical_test.go — the canonical JSON form's own contract: spelling
// independence (the digest depends on it), strictness (duplicate keys,
// trailing junk, malformed tails, empty input are refused), and exact-number
// normalization (no float64 hop — big numbers that a double cannot tell apart
// stay apart, while 1 / 1.0 / 1e0 fold to one canonical "1").

import (
	"strings"
	"testing"
)

func TestCanonicalizeJSONNormalizesSpellings(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"keys sorted", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"keys sorted deep", `{"z":{"d":1,"c":2},"a":[3,{"y":1,"x":2}]}`, `{"a":[3,{"x":2,"y":1}],"z":{"c":2,"d":1}}`},
		{"whitespace everywhere", " { \"a\" : [ 1 , 2 ] } ", `{"a":[1,2]}`},
		{"empty object/array", `{"a":[],"b":{}}`, `{"a":[],"b":{}}`},
		{"string escapes folded", `{"a":"\u0041b"}`, `{"a":"Ab"}`},
		{"number plain", `{"a":1}`, `{"a":1}`},
		{"number fraction zero", `{"a":1.0}`, `{"a":1}`},
		{"number exponent", `{"a":1e2}`, `{"a":100}`},
		{"number big exponent upper", `{"a":1E+2}`, `{"a":100}`},
		{"number trailing fraction", `{"a":0.300}`, `{"a":0.3}`},
		{"number small", `{"a":2.5e-1}`, `{"a":0.25}`},
		{"number negative", `{"a":-2.50}`, `{"a":-2.5}`},
		{"number negative zero", `{"a":-0}`, `{"a":0}`},
		{"number negative zero decimal", `{"a":-0.0e0}`, `{"a":0}`},
		{"number zero exponent", `{"a":0e0}`, `{"a":0}`},
		{"number leading frac zeros", `{"a":0.001}`, `{"a":0.001}`},
		{"number big exact int", `{"a":123456789012345678901234567890}`, `{"a":123456789012345678901234567890}`},
		{"number e-notation big int", `{"a":12345678901234567890123456789e1}`, `{"a":123456789012345678901234567890}`},
		{"bool and null", `{"a":true,"b":null}`, `{"a":true,"b":null}`},
		{"root array order kept", `[3,1,2]`, `[3,1,2]`},
		{"boundary exponent", `{"a":1e1000}`, `{"a":1` + strings.Repeat("0", 1000) + `}`},
		{"boundary small exponent", `{"a":1e-1000}`, `{"a":0.` + strings.Repeat("0", 999) + `1}`},
	}
	for _, c := range cases {
		got, err := canonicalizeJSON([]byte(c.in))
		if err != nil {
			t.Errorf("%s: canonicalizeJSON(%s): %v", c.name, c.in, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s: canonicalizeJSON(%s)\n  got  %s\n  want %s", c.name, c.in, got, c.want)
		}
	}
}

// TestCanonicalizeJSONIsASpellingIndependentFixedPoint is the property the
// digest depends on: two texts that name the same JSON value canonicalize to
// the same bytes, and the canonical form is its own canonical form (so
// store → jsonb → fetch → canonicalize returns to exactly the stored bytes).
func TestCanonicalizeJSONIsASpellingIndependentFixedPoint(t *testing.T) {
	a := `{"type":"object","properties":{"q":{"minimum":0.50},"p":{"enum":[1.0,2]}},"required":["p"]}`
	b := `{"required":["p"],"properties":{"p":{"enum":[1,2.0]},"q":{"minimum":0.5}},"type":"object"}`
	ca, err := canonicalizeJSON([]byte(a))
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	cb, err := canonicalizeJSON([]byte(b))
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if string(ca) != string(cb) {
		t.Errorf("same value, two canonical forms:\n%s\n%s", ca, cb)
	}
	again, err := canonicalizeJSON(ca)
	if err != nil {
		t.Fatalf("re-canonicalize: %v", err)
	}
	if string(again) != string(ca) {
		t.Errorf("canonical form is not a fixed point:\n%s\n%s", ca, again)
	}
}

func TestCanonicalizeJSONRefusesAmbiguousAndMalformedInput(t *testing.T) {
	for name, in := range map[string]string{
		"duplicate key":            `{"a":1,"a":2}`,
		"duplicate key nested":     `{"x":{"a":1,"a":2}}`,
		"duplicate key via escape": `{"a":1,"\u0061":2}`,
		"duplicate key in array":   `[{"a":1,"a":2}]`,
		"trailing value":           `{} {}`,
		"trailing garbage":         `{} garbage`,
		"trailing partial object":  `{"a":1}}`,
		"trailing partial array":   `[1,2]]`,
		"trailing bare delimiter":  `"x" }`,
		"empty input":              ``,
		"whitespace-only input":    "  \t\n",
		"unterminated object":      `{"a":1`,
		"lone delimiter":           `}`,
		"number exponent too far":  `{"a":1e1001}`,
		"number mantissa too long": `{"a":` + strings.Repeat("9", 1001) + `}`,
	} {
		got, err := canonicalizeJSON([]byte(in))
		if err == nil {
			t.Errorf("%s: canonicalizeJSON(%s) = %s, want an error", name, in, got)
			continue
		}
		if name == "duplicate key" || name == "duplicate key nested" ||
			name == "duplicate key via escape" || name == "duplicate key in array" {
			if !strings.Contains(err.Error(), "duplicate") {
				t.Errorf("%s: error should name the duplicate key; got %v", name, err)
			}
		}
	}
}

// TestNormalizeJSONNumberExactness pins the exact-decimal property the
// review asked for: two numbers a float64 cannot distinguish stay distinct.
func TestNormalizeJSONNumberExactness(t *testing.T) {
	big1 := "123456789012345678901234567890"
	big2 := "123456789012345678901234567891"
	n1, err := normalizeJSONNumber(big1)
	if err != nil {
		t.Fatalf("normalize %s: %v", big1, err)
	}
	n2, err := normalizeJSONNumber(big2)
	if err != nil {
		t.Fatalf("normalize %s: %v", big2, err)
	}
	if n1 == n2 {
		t.Errorf("two distinct exact numbers collapsed: %s and %s both -> %s (float64 loss)", big1, big2, n1)
	}
	// 1, 1.0 and 1e0 are one number.
	one, err := normalizeJSONNumber("1.0")
	if err != nil {
		t.Fatal(err)
	}
	exp, err := normalizeJSONNumber("1e0")
	if err != nil {
		t.Fatal(err)
	}
	if one != "1" || exp != "1" {
		t.Errorf("1.0 -> %s, 1e0 -> %s; both must be 1", one, exp)
	}
}
