package skillregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// decodeStrictOne decodes exactly ONE JSON document from raw into target,
// refusing unknown fields at every level, refusing duplicate keys at every
// level, and refusing trailing content — whether that content is another
// well-formed value or malformed text.
//
// It is the closed-bundle/closed-contract/closed-schema decode: a document
// cannot carry a key the type does not declare, which is the mechanism behind
// "no dynamic upstream fetch" and "no symlinks" — there is no field to carry
// such an instruction, and an attempted one is a named error instead.
//
// The decode runs on the CANONICAL form (canonicalizeJSON), not on the raw
// bytes: canonicalization is where duplicate keys, trailing junk and
// out-of-bounds numbers are refused, and what reaches the struct decoder is
// already single-value and spelling-normal. The canonical bytes are returned
// so callers that need to inspect the document (e.g. compileSchema's explicit
// null-keyword pass) see exactly what was decoded.
//
// what names the document for error messages ("bundle", "contract",
// "schema at $.items", …) and supported lists the keys the target accepts, so
// an unknown-key refusal names the legal set rather than only the offending
// key.
// validateExactKeys compensates for encoding/json's case-insensitive struct matching.
func validateExactKeys(raw []byte, what string) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	allowed := map[string]bool{}
	schema := strings.HasPrefix(what, "schema")
	switch {
	case schema:
		for _, k := range SupportedSchemaKeywords() {
			allowed[k] = true
		}
	case what == "contract":
		for _, k := range []string{"capabilities", "input_schema", "params_schema", "output_schema", "runtime"} {
			allowed[k] = true
		}
	case what == "bundle":
		return validateBundleKeys(v, "bundle")
	default:
		return nil
	}
	if schema {
		return validateExactObject(v, allowed, what, true)
	}
	if what == "contract" {
		if obj, ok := v.(map[string]any); ok {
			if r, ok := obj["runtime"]; ok {
				if err := validateExactObject(r, map[string]bool{"interactive": true}, "contract.runtime", false); err != nil {
					return err
				}
			}
		}
	}
	return validateExactObject(v, allowed, what, false)
}

func validateBundleKeys(v any, where string) error {
	if list, ok := v.([]any); ok {
		for i, child := range list {
			childWhere := fmt.Sprintf("%s[%d]", where, i)
			if strings.HasSuffix(where, "[]") {
				childWhere = where
			}
			if err := validateBundleKeys(child, childWhere); err != nil {
				return err
			}
		}
		return nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	allowed := map[string]bool{}
	switch where {
	case "bundle":
		for _, k := range []string{"entry", "files", "provenance", "license"} {
			allowed[k] = true
		}
	case "bundle.files[]":
		for _, k := range []string{"path", "content", "encoding"} {
			allowed[k] = true
		}
	case "bundle.provenance":
		for _, k := range []string{"source", "upstream_url", "upstream_commit", "upstream_license", "imported_at", "notes"} {
			allowed[k] = true
		}
	case "bundle.license":
		for _, k := range []string{"name", "notice", "url"} {
			allowed[k] = true
		}
	default:
		return nil
	}
	for k, child := range obj {
		if !allowed[k] {
			return fmt.Errorf("%s uses key %q with incorrect case or unsupported spelling; supported keys include %v", where, k, allowed)
		}
		// Bundle fields are concrete values, not nullable sentinels. Accepting
		// an explicit null lets encoding/json turn it into an omitted zero
		// value (notably provenance.notes and license.notice), weakening the
		// closed input vocabulary after exact-key validation has run.
		if child == nil {
			return fmt.Errorf("%s key %q must not be null", where, k)
		}
		next := where + "." + k
		if k == "files" {
			next = "bundle.files[]"
		}
		if err := validateBundleKeys(child, next); err != nil {
			return err
		}
	}
	return nil
}

func validateExactObject(v any, allowed map[string]bool, where string, schema bool) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for k, child := range obj {
		if !allowed[k] {
			return fmt.Errorf("%s uses key %q with incorrect case or unsupported spelling; the supported keys include %v", where, k, allowed)
		}
		if schema {
			switch k {
			case "properties":
				if p, ok := child.(map[string]any); ok {
					for name, node := range p {
						if err := validateExactObject(node, allowed, where+".properties["+name+"]", true); err != nil {
							return err
						}
					}
				}
			case "items":
				if err := validateExactObject(child, allowed, where+".items", true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func decodeStrictOne(raw []byte, target any, what string, supported []string) ([]byte, error) {
	canon, err := canonicalizeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", what, err)
	}
	if err := validateExactKeys(canon, what); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(canon))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		// encoding/json reports an unknown field as a plain error with the
		// text `json: unknown field "x"`; there is no typed error to As() on,
		// so the text is the contract. If that text ever changes, the tests
		// that assert the fail-closed behaviour go red and this branch gets
		// fixed — the behaviour being asserted (named refusal, not a silent
		// skip) is the part that matters.
		if strings.Contains(err.Error(), "unknown field") {
			return nil, fmt.Errorf("%s uses a key this registry does not support (%v); the supported keys are: %v",
				what, err, supported)
		}
		return nil, fmt.Errorf("%s is not valid: %w", what, err)
	}
	// Exactly one value. canonicalizeJSON already enforces this over the raw
	// input, so this second check is defense in depth for the (canonical,
	// single-value-by-construction) bytes actually decoded here: the token
	// after the document must be io.EOF exactly — a nil token means a second
	// document, and any other error means malformed trailing text. NEITHER
	// reads as success.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s has trailing content after the JSON document", what)
	}
	return canon, nil
}
