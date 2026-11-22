package jmap

import "strings"

// ApplyPatch applies a PatchObject (RFC 8620 §5.3) to obj in place.  A null
// value removes the property; anything else sets it.  Callers that have
// defaults for properties must reinstate them afterwards, since this
// function cannot know them.  Structural violations are reported as an
// invalidPatch SetError, and obj may be partly modified when one is
// returned, so callers should discard it in that case.
func ApplyPatch(obj map[string]any, patch map[string]any) *SetError {
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	for i, a := range keys {
		for j, b := range keys {
			if i != j && strings.HasPrefix(b, a+"/") {
				return &SetError{
					Type:        SetErrInvalidPatch,
					Description: "patch path " + a + " is a prefix of " + b,
				}
			}
		}
	}

	for k, val := range patch {
		tokens := strings.Split(k, "/")
		for i := range tokens {
			tokens[i] = UnescapeToken(tokens[i])
			if tokens[i] == "" {
				return &SetError{Type: SetErrInvalidPatch, Description: "empty path segment in " + k}
			}
		}
		parent := obj
		for _, t := range tokens[:len(tokens)-1] {
			child, ok := parent[t]
			if !ok {
				return &SetError{Type: SetErrInvalidPatch, Description: "path " + k + " does not exist"}
			}
			m, ok := child.(map[string]any)
			if !ok {
				return &SetError{Type: SetErrInvalidPatch, Description: "path " + k + " passes through a non-object"}
			}
			parent = m
		}
		last := tokens[len(tokens)-1]
		if val == nil {
			delete(parent, last)
		} else {
			parent[last] = val
		}
	}
	return nil
}
