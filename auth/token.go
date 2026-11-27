package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash/crc32"
	"math/big"
	"strings"
)

// A bearer token looks like
//
//	<prefix><43 base62 characters of randomness><6 base62 characters of CRC32>
//
// following the scheme GitHub uses for its tokens.  The prefix, which each
// server chooses for itself, like "taskmaster-", lets a secret scanner find
// a leaked token, and the checksum of the random part lets it tell a real
// token from a lookalike without asking the server.  Base62 keeps
// punctuation out of the body, so the token is one word to a regex.  43
// base62 characters hold 256 bits, and 6 hold 32.  -- claude, 2026-11-07
const (
	tokenRandLen  = 43
	tokenCheckLen = 6
	base62        = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// NewToken makes a random bearer token with the given prefix.
func NewToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	body := encodeBase62(new(big.Int).SetBytes(b), tokenRandLen)
	return prefix + body + tokenChecksum(body), nil
}

// WellFormedToken reports whether token has the shape [NewToken] gives it for
// the prefix, and a correct checksum.  It says nothing about whether any
// user holds it.
func WellFormedToken(prefix, token string) bool {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok || len(rest) != tokenRandLen+tokenCheckLen {
		return false
	}
	for i := range len(rest) {
		if strings.IndexByte(base62, rest[i]) < 0 {
			return false
		}
	}
	body, check := rest[:tokenRandLen], rest[tokenRandLen:]
	return tokenChecksum(body) == check
}

// HashToken gives the digest a bearer credential stores for a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func tokenChecksum(body string) string {
	return encodeBase62(big.NewInt(int64(crc32.ChecksumIEEE([]byte(body)))), tokenCheckLen)
}

// encodeBase62 writes n in base62, left-padded with zeros to width
// characters.  n must fit.
func encodeBase62(n *big.Int, width int) string {
	out := make([]byte, width)
	n = new(big.Int).Set(n)
	radix, digit := big.NewInt(62), new(big.Int)
	for i := width - 1; i >= 0; i-- {
		n.DivMod(n, radix, digit)
		out[i] = base62[digit.Int64()]
	}
	return string(out)
}
