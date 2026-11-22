package jmap

import (
	"fmt"
	"strconv"
	"strings"
)

// UnescapeToken reverses JSON Pointer token escaping (RFC 6901 §4).
func UnescapeToken(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	return strings.ReplaceAll(s, "~0", "~")
}

// ResolvePointer evaluates a JSON Pointer against v.  Beyond RFC 6901, a "*"
// token applied to an array maps the remainder of the pointer over every
// element and flattens one level of array results, as RFC 8620 §3.7
// requires for result references.
func ResolvePointer(v any, pointer string) (any, error) {
	if pointer == "" {
		return v, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("pointer %q does not start with /", pointer)
	}
	return resolveTokens(v, strings.Split(pointer[1:], "/"))
}

func resolveTokens(v any, tokens []string) (any, error) {
	for i, raw := range tokens {
		tok := UnescapeToken(raw)
		switch cur := v.(type) {
		case map[string]any:
			next, ok := cur[tok]
			if !ok {
				return nil, fmt.Errorf("no property %q", tok)
			}
			v = next
		case []any:
			if tok == "*" {
				out := []any{}
				for _, el := range cur {
					r, err := resolveTokens(el, tokens[i+1:])
					if err != nil {
						return nil, err
					}
					if arr, ok := r.([]any); ok {
						out = append(out, arr...)
					} else {
						out = append(out, r)
					}
				}
				return out, nil
			}
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(cur) {
				return nil, fmt.Errorf("bad array index %q", tok)
			}
			v = cur[idx]
		default:
			return nil, fmt.Errorf("cannot descend into %T with %q", v, tok)
		}
	}
	return v, nil
}
