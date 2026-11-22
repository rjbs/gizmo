package jmap

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustObject(t *testing.T, src string) map[string]any {
	t.Helper()
	m, err := DecodeObject([]byte(src))
	if err != nil {
		t.Fatalf("bad test JSON %q: %v", src, err)
	}
	return m
}

// checkPatch applies patch to obj and compares against want, or against an
// expected SetError type when wantErr is non-empty.
func checkPatch(t *testing.T, name, obj, patch, want, wantErr string) {
	t.Helper()
	o := mustObject(t, obj)
	err := ApplyPatch(o, mustObject(t, patch))
	switch {
	case wantErr == "" && err != nil:
		t.Errorf("%s: unexpected error %v", name, err)
	case wantErr != "" && err == nil:
		t.Errorf("%s: expected %s error, got none", name, wantErr)
	case wantErr != "" && err.Type != wantErr:
		t.Errorf("%s: expected %s error, got %s", name, wantErr, err.Type)
	case wantErr == "":
		if w := mustObject(t, want); !reflect.DeepEqual(o, w) {
			got, _ := json.Marshal(o)
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
}

func TestApplyPatch(t *testing.T) {
	checkPatch(t, "replace top-level",
		`{"a": 1, "b": 2}`, `{"a": 3}`, `{"a": 3, "b": 2}`, "")
	checkPatch(t, "add top-level",
		`{"a": 1}`, `{"c": true}`, `{"a": 1, "c": true}`, "")
	checkPatch(t, "remove top-level",
		`{"a": 1, "b": 2}`, `{"a": null}`, `{"b": 2}`, "")
	checkPatch(t, "remove absent is a no-op",
		`{"a": 1}`, `{"z": null}`, `{"a": 1}`, "")
	checkPatch(t, "nested set",
		`{"o": {"x": 1}}`, `{"o/y": 2}`, `{"o": {"x": 1, "y": 2}}`, "")
	checkPatch(t, "nested remove",
		`{"o": {"x": 1, "y": 2}}`, `{"o/y": null}`, `{"o": {"x": 1}}`, "")
	checkPatch(t, "escaped slash in key",
		`{"o": {"a/b": 1}}`, `{"o/a~1b": 2}`, `{"o": {"a/b": 2}}`, "")
	checkPatch(t, "replace whole object",
		`{"o": {"x": 1}}`, `{"o": {"y": 2}}`, `{"o": {"y": 2}}`, "")
	checkPatch(t, "missing intermediate",
		`{"o": {}}`, `{"o/p/q": 1}`, ``, SetErrInvalidPatch)
	checkPatch(t, "through an array",
		`{"arr": [1, 2]}`, `{"arr/0": 5}`, ``, SetErrInvalidPatch)
	checkPatch(t, "prefix conflict",
		`{"o": {"x": 1}}`, `{"o": {}, "o/x": 2}`, ``, SetErrInvalidPatch)
}

func checkPointer(t *testing.T, doc, pointer, want string) {
	t.Helper()
	v, err := ResolvePointer(mustObject(t, doc), pointer)
	if err != nil {
		t.Errorf("%s: %v", pointer, err)
		return
	}
	got, _ := json.Marshal(v)
	if string(got) != want {
		t.Errorf("%s: got %s, want %s", pointer, got, want)
	}
}

func TestResolvePointer(t *testing.T) {
	doc := `{"ids": ["a", "b"], "list": [{"id": "x", "tags": [1, 2]}, {"id": "y", "tags": [3]}], "a/b": 1}`
	checkPointer(t, doc, "/ids", `["a","b"]`)
	checkPointer(t, doc, "/ids/1", `"b"`)
	checkPointer(t, doc, "/list/*/id", `["x","y"]`)
	checkPointer(t, doc, "/list/*/tags", `[1,2,3]`)
	checkPointer(t, doc, "/a~1b", `1`)

	// An id may be all digits, or read as a number, and is still a key.
	ids := `{"created": {"k": {"id": "007"}}, "byId": {"123456789012": {"n": 1}, "0": {"n": 2}, "123e456789ab": {"n": 3}}}`
	checkPointer(t, ids, "/created/k/id", `"007"`)
	checkPointer(t, ids, "/byId/123456789012/n", `1`)
	checkPointer(t, ids, "/byId/0/n", `2`)
	checkPointer(t, ids, "/byId/123e456789ab/n", `3`)
	if _, err := ResolvePointer(mustObject(t, doc), "/nope"); err == nil {
		t.Error("expected error for missing property")
	}
}
