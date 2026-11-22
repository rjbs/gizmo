package auth

import (
	"strings"
	"testing"
)

func checkWellFormed(t *testing.T, name, token string, want bool) {
	t.Helper()
	if got := WellFormedToken("taskmaster-", token); got != want {
		t.Errorf("%s: WellFormedToken(%q) = %v, want %v", name, token, got, want)
	}
}

func TestTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		tok, err := NewToken("taskmaster-")
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != 60 || !strings.HasPrefix(tok, "taskmaster-") || strings.ContainsAny(tok[11:], "-_") {
			t.Fatalf("unexpected token shape: %q", tok)
		}
		checkWellFormed(t, "fresh token", tok, true)
		if seen[tok] {
			t.Fatalf("token repeated: %q", tok)
		}
		seen[tok] = true
	}

	tok, _ := NewToken("taskmaster-")
	swap := func(i int) string {
		c := byte('A')
		if tok[i] == 'A' {
			c = 'B'
		}
		return tok[:i] + string(c) + tok[i+1:]
	}
	checkWellFormed(t, "changed random part", swap(20), false)
	checkWellFormed(t, "changed checksum", swap(len(tok)-1), false)
	checkWellFormed(t, "wrong prefix", "taskmastr-"+tok[11:]+"0", false)
	checkWellFormed(t, "old prefix", "tm_"+tok[11:], false)
	checkWellFormed(t, "truncated", tok[:59], false)
	checkWellFormed(t, "punctuation", tok[:30]+"!"+tok[31:], false)
}
