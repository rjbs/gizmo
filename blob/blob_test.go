package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// failing is a stream that gives some bytes and then an error.
type failing struct {
	r   io.Reader
	err error
}

func (f *failing) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		err = f.err
	}
	return n, err
}

func TestReceive(t *testing.T) {
	dir := t.TempDir()
	got, err := Receive(dir, strings.NewReader("flour, sugar, butter"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("flour, sugar, butter"))
	if want := hex.EncodeToString(sum[:]); got.SHA256 != want {
		t.Errorf("the digest is %s, want %s", got.SHA256, want)
	}
	data, err := os.ReadFile(got.Path)
	if err != nil || string(data) != "flour, sugar, butter" || got.Size != 20 || len(got.SHA256) != 64 {
		t.Errorf("received %q, size %d, digest %q, error %v", data, got.Size, got.SHA256, err)
	}
	if !strings.HasPrefix(got.Path, dir) {
		t.Errorf("the file should be in the directory given, but is %s", got.Path)
	}
	again, err := Receive(dir, strings.NewReader("flour, sugar, butter"))
	if err != nil || again.SHA256 != got.SHA256 || again.Path == got.Path {
		t.Errorf("the same bytes again: digest %q at %s, error %v; want the same digest in another file", again.SHA256, again.Path, err)
	}
	other, err := Receive(dir, strings.NewReader("flour, sugar, lard"))
	if err != nil || other.SHA256 == got.SHA256 {
		t.Errorf("other bytes: digest %q, error %v; want a different digest", other.SHA256, err)
	}

	before, _ := os.ReadDir(dir)
	_, err = Receive(dir, &failing{r: strings.NewReader("half of an upl"), err: ErrTooLarge})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("a stream that fails: got %v, want ErrTooLarge", err)
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Errorf("a stream that failed left a file behind: %d files, there were %d", len(after), len(before))
	}
}
