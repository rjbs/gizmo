package jmaptest

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// Response is a JMAP response: the sentences of its methodResponses, and
// what came with them.
type Response struct {
	t         testing.TB
	sentences []*Sentence

	// CreatedIDs is the response's createdIds, or nil if it had none.
	CreatedIDs   map[string]string
	SessionState string
}

func parseResponse(t testing.TB, body []byte) *Response {
	t.Helper()
	var raw struct {
		MethodResponses []json.RawMessage `json:"methodResponses"`
		CreatedIDs      map[string]string `json:"createdIds"`
		SessionState    string            `json:"sessionState"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.MethodResponses == nil {
		t.Fatalf("not a JMAP response: %s", body)
	}
	r := &Response{t: t, CreatedIDs: raw.CreatedIDs, SessionState: raw.SessionState}
	for _, item := range raw.MethodResponses {
		var parts []json.RawMessage
		s := &Sentence{t: t}
		if err := json.Unmarshal(item, &parts); err != nil || len(parts) != 3 ||
			json.Unmarshal(parts[0], &s.Name) != nil || json.Unmarshal(parts[2], &s.CallID) != nil {
			t.Fatalf("not a method response: %s", item)
		}
		s.Raw = parts[1]
		s.Args = decodeObject(t, parts[1])
		r.sentences = append(r.sentences, s)
	}
	return r
}

// Sentences returns every sentence of the response.
func (r *Response) Sentences() []*Sentence { return r.sentences }

// Sentence returns the nth sentence of the response, counting from zero.
func (r *Response) Sentence(n int) *Sentence {
	r.t.Helper()
	if n < 0 || n >= len(r.sentences) {
		r.t.Fatalf("there is no sentence %d; the response has %d:\n%s", n, len(r.sentences), r)
	}
	return r.sentences[n]
}

// SingleSentence returns the only sentence of the response, which must have
// the given name.
func (r *Response) SingleSentence(name string) *Sentence {
	r.t.Helper()
	if len(r.sentences) != 1 {
		r.t.Fatalf("expected a single sentence, but the response has %d:\n%s", len(r.sentences), r)
	}
	return r.sentences[0].AssertNamed(name)
}

// SentenceNamed returns the one sentence of the response with the given
// name.
func (r *Response) SentenceNamed(name string) *Sentence {
	r.t.Helper()
	var found []*Sentence
	for _, s := range r.sentences {
		if s.Name == name {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		r.t.Fatalf("expected one sentence named %s, but found %d:\n%s", name, len(found), r)
	}
	return found[0]
}

// AssertSentences returns the sentences of the response, of which there must
// be n.
func (r *Response) AssertSentences(n int) []*Sentence {
	r.t.Helper()
	if len(r.sentences) != n {
		r.t.Fatalf("expected %d sentences, but the response has %d:\n%s", n, len(r.sentences), r)
	}
	return r.sentences
}

// Paragraphs groups the sentences by method call: each paragraph is a run of
// sentences with the same call id.
func (r *Response) Paragraphs() [][]*Sentence {
	var out [][]*Sentence
	for i, s := range r.sentences {
		if i == 0 || s.CallID != r.sentences[i-1].CallID {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], s)
	}
	return out
}

// Paragraph returns the sentences answering the method call with the given
// call id, of which there must be at least one.
func (r *Response) Paragraph(callID string) []*Sentence {
	r.t.Helper()
	for _, p := range r.Paragraphs() {
		if p[0].CallID == callID {
			return p
		}
	}
	r.t.Fatalf("there is no paragraph for call id %q:\n%s", callID, r)
	return nil
}

// String gives the response's sentences as JSON, one to a line.
func (r *Response) String() string {
	lines := make([]string, len(r.sentences))
	for i, s := range r.sentences {
		lines[i] = s.String()
	}
	return strings.Join(lines, "\n")
}

// Sentence is one method response.
type Sentence struct {
	t testing.TB

	Name   string
	CallID string

	// Args is the response's arguments, decoded, with numbers kept as
	// json.Number.  Raw is the same as it was sent, which is what tells a
	// string from a number: the id "123" from the number 123.
	Args map[string]any
	Raw  json.RawMessage
}

// String gives the sentence as JSON.
func (s *Sentence) String() string {
	b, _ := json.Marshal([]any{s.Name, s.Raw, s.CallID})
	return string(b)
}

// AssertNamed returns the sentence, which must have the given name.
func (s *Sentence) AssertNamed(name string) *Sentence {
	s.t.Helper()
	if s.Name != name {
		s.t.Fatalf("expected a sentence named %s, but got %s", name, s)
	}
	return s
}

// AssertError returns the sentence, which must be an error of the given
// type.
func (s *Sentence) AssertError(typ string) *Sentence {
	s.t.Helper()
	if s.Name != "error" || s.Args["type"] != typ {
		s.t.Fatalf("expected an error of type %s, but got %s", typ, s)
	}
	return s
}

// Decode decodes the sentence's arguments into a value of the test's own
// type.
func (s *Sentence) Decode(into any) {
	s.t.Helper()
	if err := json.Unmarshal(s.Raw, into); err != nil {
		s.t.Fatalf("decoding %s: %v", s, err)
	}
}

// Str returns the argument of the given name, which must be a string.
func (s *Sentence) Str(name string) string {
	s.t.Helper()
	v, ok := s.Args[name].(string)
	if !ok {
		s.t.Fatalf("expected a string for %q, but got %s", name, s)
	}
	return v
}

// Strs returns the argument of the given name, which must be an array of
// strings.
func (s *Sentence) Strs(name string) []string {
	s.t.Helper()
	list, ok := s.Args[name].([]any)
	if !ok {
		s.t.Fatalf("expected an array for %q, but got %s", name, s)
	}
	out := make([]string, len(list))
	for i, v := range list {
		if out[i], ok = v.(string); !ok {
			s.t.Fatalf("expected an array of strings for %q, but got %s", name, s)
		}
	}
	return out
}

// AsSet returns the sentence as the response to a /set, or to another method
// that answers in the same shape, such as an /import.
func (s *Sentence) AsSet() *Set {
	s.t.Helper()
	if s.Name == "error" {
		s.t.Fatalf("expected the response to a /set, but got %s", s)
	}
	return &Set{Sentence: s}
}

// Set is a sentence answering a /set.
type Set struct {
	*Sentence
}

func (s *Set) OldState() string { s.t.Helper(); return s.Str("oldState") }
func (s *Set) NewState() string { s.t.Helper(); return s.Str("newState") }

func (s *Set) objects(name string) map[string]any {
	m, _ := s.Args[name].(map[string]any)
	return m
}

// Created returns what the server said of the object created under a
// creation id, which must have been created.
func (s *Set) Created(creationID string) map[string]any {
	s.t.Helper()
	o, ok := s.objects("created")[creationID].(map[string]any)
	if !ok {
		s.t.Fatalf("nothing was created as %q: %s", creationID, s)
	}
	return o
}

// CreatedID returns the id of the object created under a creation id, which
// must have been created.
func (s *Set) CreatedID(creationID string) string {
	s.t.Helper()
	id, ok := s.Created(creationID)["id"].(string)
	if !ok {
		s.t.Fatalf("the object created as %q has no id that is a string: %s", creationID, s)
	}
	return id
}

// UpdatedIDs returns the ids of the objects updated, sorted.
func (s *Set) UpdatedIDs() []string { return sortedKeys(s.objects("updated")) }

// DestroyedIDs returns the ids of the objects destroyed, sorted.
func (s *Set) DestroyedIDs() []string {
	list, _ := s.Args["destroyed"].([]any)
	out := []string{}
	for _, v := range list {
		if id, ok := v.(string); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Set) setError(list, key string) map[string]any {
	s.t.Helper()
	e, ok := s.objects(list)[key].(map[string]any)
	if !ok {
		s.t.Fatalf("expected %q in %s, but got %s", key, list, s)
	}
	return e
}

// NotCreated returns the error for a creation id, which must have failed.
func (s *Set) NotCreated(creationID string) map[string]any {
	s.t.Helper()
	return s.setError("notCreated", creationID)
}

// NotUpdated returns the error for an id, whose update must have failed.
func (s *Set) NotUpdated(id string) map[string]any {
	s.t.Helper()
	return s.setError("notUpdated", id)
}

// NotDestroyed returns the error for an id, whose destruction must have
// failed.
func (s *Set) NotDestroyed(id string) map[string]any {
	s.t.Helper()
	return s.setError("notDestroyed", id)
}

// AssertNoErrors returns the set, in which nothing may have failed.
func (s *Set) AssertNoErrors() *Set {
	s.t.Helper()
	var found []string
	for _, list := range []string{"notCreated", "notUpdated", "notDestroyed"} {
		if errs := s.objects(list); len(errs) > 0 {
			b, _ := json.Marshal(errs)
			found = append(found, list+": "+string(b))
		}
	}
	if len(found) > 0 {
		s.t.Fatalf("errors in the %s response:\n%s", s.Name, strings.Join(found, "\n"))
	}
	return s
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
