package skillregistry

// canonical.go — the registry's canonical JSON form: the ONE serialization a
// stored bundle/contract (and therefore the version digest) is defined over,
// and the ONE normalizer semantic-equality comparisons run on.
//
// Why a canonical form exists at all: skill_versions.bundle and .contract are
// JSONB columns. PostgreSQL jsonb does not preserve the caller's bytes — it
// re-serializes object keys in ITS order (length first, then bytewise) with
// ITS spacing, and rewrites number literals through `numeric`. If the digest
// were computed over "whatever bytes the caller sent", the digest stored at
// publish time would not reproduce from the bytes a later fetch returns, and
// every re-computation (Batch 4A's seed/import content check, any future
// audit) would report a mismatch that is not a content change.
//
// The canonical form is defined so that it is a FIXED POINT of that round
// trip: canonicalize(parse(x)) depends only on the JSON VALUE of x, never on
// its spelling. Two spellings of the same value — reordered keys, 1 vs 1.0 vs
// 1e0, 0.300 vs 0.3 — canonicalize to the SAME bytes, and a value that is
// already canonical stays byte-identical. The digest is computed over exactly
// these bytes, so publish-time and post-fetch digests agree by construction.
//
// Numbers are normalized as EXACT DECIMALS, never through float64: two JSON
// numbers are the same canonical string iff they are the same mathematical
// value. float64 would collapse 12345678901234567890123 and its +1 neighbour
// (both round to the same double) and turn "distinct" into "duplicate"; the
// decimal form keeps them apart while still folding 1 / 1.0 / 1e0 to "1".
// The expansion is bounded (see maxCanonicalMantissaDigits /
// maxCanonicalExponent): a number that would need a thousand-and-first digit
// to write is refused with a named error rather than silently expanded into a
// megabyte literal — well inside the sizes jsonb `numeric` accepts, so a
// canonical number always survives the column it is stored in.
//
// The canonical form also carries three strictness rules the plain
// encoding/json decoder does not have, all of them load-bearing:
//
//   - DUPLICATE KEYS are refused at every object level. Go's decoder keeps
//     the LAST duplicate silently and PostgreSQL jsonb does the same, but the
//     two agree only by accident of implementation — a document whose meaning
//     depends on which copy wins is ambiguous, and an ambiguous document is
//     not a contract. The refusal names the key.
//   - EXACTLY ONE VALUE: a second document pasted after the first is refused,
//     AND so is malformed trailing text (`}{"a":1}` followed by `garbage`
//     must not decode "successfully" because the trailing bytes merely fail
//     to tokenize).
//   - EMPTY input is refused (a schema/bundle/contract is never "nothing").

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Bounds on the canonical NUMBER form. A number is refused (not silently
// re-spelled) when normalizing it would exceed either bound; both sit far
// inside what jsonb numeric storage accepts (131072 integer digits, 16383
// fraction digits), so a canonical number can always be stored and fetched
// back unchanged. Schemas describe prompt-scale parameters; a number needing
// a thousand significant digits or a ±1000 exponent is not one.
const (
	maxCanonicalMantissaDigits = 1000
	maxCanonicalExponent       = 1000
)

// canonicalizeJSON parses exactly one JSON value from raw and returns its
// canonical serialization. It fails closed on duplicate keys, trailing
// content (well-formed or malformed), empty input, and numbers outside the
// canonical bounds.
func canonicalizeJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("the JSON document is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers as literals: the exact-decimal path depends on it
	var out bytes.Buffer
	if err := writeCanonicalValue(&out, dec); err != nil {
		return nil, err
	}
	// Exactly one value, and the tail must be WELL-FORMED nothing. A nil
	// error here means a second value followed the first; an error that is
	// not io.EOF is malformed trailing text — both are refusals. (This is the
	// strictness the review's "decodeStrictOne accepts any Token error"
	// finding asks for: a trailing-syntax error must never read as success.)
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("trailing content after the JSON document")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("malformed trailing content after the JSON document: %w", err)
	}
	return out.Bytes(), nil
}

// writeCanonicalValue consumes one value from dec and writes its canonical
// form to out.
func writeCanonicalValue(out *bytes.Buffer, dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return writeCanonicalObject(out, dec)
		case '[':
			return writeCanonicalArray(out, dec)
		default:
			// '}' or ']': the decoder hands these to us only for malformed
			// documents (a value position holding a closing delimiter).
			return fmt.Errorf("stray %q cannot start a JSON value", string(rune(t)))
		}
	case json.Number:
		canonical, err := normalizeJSONNumber(string(t))
		if err != nil {
			return err
		}
		out.WriteString(canonical)
	case string:
		// Re-encode through json.Marshal so every string takes ONE spelling:
		// the decoder has already unescaped the input, and Marshal re-escapes
		// with Go's deterministic rules. An input spelling "\u0041" and an
		// input spelling "A" therefore canonicalize identically.
		encoded, err := json.Marshal(t)
		if err != nil {
			return err
		}
		out.Write(encoded)
	case bool:
		if t {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return fmt.Errorf("unsupported JSON token %T", tok)
	}
	return nil
}

// writeCanonicalObject consumes the remainder of an object from dec (past its
// opening '{') and writes the canonical form: keys sorted, duplicate keys
// refused, values canonicalized.
func writeCanonicalObject(out *bytes.Buffer, dec *json.Decoder) error {
	type member struct {
		key   string
		value []byte
	}
	var members []member
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("object key is not a string: %v", keyTok)
		}
		// The decoder unescapes before we see the key, so this also catches a
		// duplicate spelled once literally and once as an escape ("a" vs
		// "\u0061") — a plain byte-level scan of the raw text would miss it.
		if seen[key] {
			return fmt.Errorf("duplicate object key %q", key)
		}
		seen[key] = true
		var value bytes.Buffer
		if err := writeCanonicalValue(&value, dec); err != nil {
			return fmt.Errorf("at object key %q: %w", key, err)
		}
		members = append(members, member{key: key, value: value.Bytes()})
	}
	if _, err := dec.Token(); err != nil { // consume the closing '}'
		return err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].key < members[j].key })
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		encodedKey, err := json.Marshal(m.key)
		if err != nil {
			return err
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')
	return nil
}

// writeCanonicalArray consumes the remainder of an array from dec (past its
// opening '[') and writes the canonical form. Order is preserved — array
// order is data, not spelling.
func writeCanonicalArray(out *bytes.Buffer, dec *json.Decoder) error {
	out.WriteByte('[')
	first := true
	for dec.More() {
		if !first {
			out.WriteByte(',')
		}
		first = false
		if err := writeCanonicalValue(out, dec); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // consume the closing ']'
		return err
	}
	out.WriteByte(']')
	return nil
}

// normalizeJSONNumber maps a JSON number literal to its canonical form: plain
// decimal, minimal digits, no exponent, "-0" folded to "0". Two literals
// normalize to the same string iff they name the same mathematical value —
// 1, 1.0, 1e0 and 0.999...-spelled-otherwise do not appear here, but 1.50
// and 1.5 do fold together, and -0 becomes 0 (JSON has no negative zero as a
// distinct value; PostgreSQL jsonb agrees — '-0'::jsonb prints 0).
//
// The value is carried as (digits, exponent) exactly as written: no float64
// hop, so 12345678901234567890123 and 12345678901234567890124 stay distinct.
func normalizeJSONNumber(literal string) (string, error) {
	s := literal
	negative := strings.HasPrefix(s, "-")
	if negative {
		s = s[1:]
	}
	mantissa, exponent := s, int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.ParseInt(s[i+1:], 10, 64)
		if err != nil {
			return "", fmt.Errorf("number %q carries an exponent that cannot be normalized", literal)
		}
		exponent = e
		mantissa = s[:i]
	}
	fraction := ""
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		fraction = mantissa[i+1:]
		mantissa = mantissa[:i]
	}
	digits := mantissa + fraction
	if digits == "" {
		return "", fmt.Errorf("number %q has no digits", literal)
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			// The decoder already validated the JSON number grammar; this
			// arm is the defense if a caller ever hands a raw literal here.
			return "", fmt.Errorf("number %q is not a decimal literal", literal)
		}
	}
	exponent -= int64(len(fraction))
	digits = strings.TrimLeft(digits, "0")
	trimmedZeros := 0
	for len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		trimmedZeros++
	}
	if digits == "" {
		// Every spelling of zero — including -0 and 0e0 — is one canonical 0.
		return "0", nil
	}
	exponent += int64(trimmedZeros)
	if len(digits) > maxCanonicalMantissaDigits {
		return "", fmt.Errorf("number %q needs %d significant digits to write; the registry's bound is %d",
			literal, len(digits), maxCanonicalMantissaDigits)
	}
	if exponent > maxCanonicalExponent || exponent < -maxCanonicalExponent {
		return "", fmt.Errorf("number %q needs a decimal exponent of %d to write; the registry's bound is ±%d",
			literal, exponent, maxCanonicalExponent)
	}
	var b strings.Builder
	if negative {
		b.WriteByte('-')
	}
	if exponent >= 0 {
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", int(exponent)))
		return b.String(), nil
	}
	point := len(digits) + int(exponent)
	if point > 0 {
		b.WriteString(digits[:point])
		b.WriteByte('.')
		b.WriteString(digits[point:])
		return b.String(), nil
	}
	b.WriteString("0.")
	b.WriteString(strings.Repeat("0", -point))
	b.WriteString(digits)
	return b.String(), nil
}
