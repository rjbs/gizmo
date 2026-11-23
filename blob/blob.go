// Package blob is where an application's blobs meet the JMAP upload and
// download endpoints (RFC 8620 §6).
//
// The application decides what a blob id is and where the bytes live, by
// implementing Store.  The endpoints, in package httpd, see to the rest: who
// may use the account, how large an upload may be, how many may run at once,
// and range requests on the way down.
//
// A blob id always names the same bytes.  RFC 8620 requires it, and the
// download endpoint tells clients they may keep what they fetch for good.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ErrNotFound is what Store.Get returns for a blob id that names nothing in
// the account.
var ErrNotFound = errors.New("blob: no such blob")

// ErrTooLarge is the error an Upload's Body gives when the upload has run
// past the largest size the server accepts.  A Store that gets it from a
// read should give up and return it, having kept nothing.
var ErrTooLarge = errors.New("blob: the upload is larger than the server accepts")

// Refusal is an error a Store returns to turn an upload down for a reason of
// its own, such as a kind of content it has no use for.  The client is sent
// the status and the detail.
type Refusal struct {
	// Status is the HTTP status to answer with, such as 415.
	Status int
	Detail string
}

func (r *Refusal) Error() string { return r.Detail }

// Upload is a blob on its way in.
type Upload struct {
	AccountID string

	// Type is the content type the client gave, which may be empty.
	Type string

	// Body is the blob's bytes.  It is to be read as a stream: an upload
	// may be far larger than there is memory for.
	Body io.Reader
}

// Uploaded describes a blob that has been stored.
type Uploaded struct {
	BlobID string
	Type   string
	Size   int64
}

// Blob is a blob on its way out.
type Blob struct {
	// Content is the blob's bytes.  It must be able to seek, so that a
	// client can ask for part of a blob.  If it is also an io.Closer, it
	// is closed when the download is done.
	Content io.ReadSeeker

	// Type is the blob's content type, used unless the client asks for
	// another.
	Type string

	// ModTime is when the blob was stored, if that is known.
	ModTime time.Time
}

// Store keeps an application's blobs.  The context of each call carries the
// caller's identity, as the context of a method call does.
type Store interface {
	// Put stores an upload and says what it was stored as.  Whether a
	// blob that nothing goes on to use is kept, and for how long, is the
	// Store's own business.
	Put(ctx context.Context, up *Upload) (*Uploaded, error)

	// Get returns the blob with the given id in the account, or
	// ErrNotFound.
	Get(ctx context.Context, accountID, blobID string) (*Blob, error)
}

// Received is an upload that has been written to a file.
type Received struct {
	// Path is the file, which is the caller's to rename into place or to
	// remove.
	Path string

	// SHA256 is the digest of the bytes, in lowercase hex.
	SHA256 string
	Size   int64
}

// Receive writes a stream to a new file in dir, working out its SHA-256 and
// its size as the bytes go by, so that a blob of any size can be taken in
// and named for its content without being held in memory or read twice.  If
// the stream fails, as it does with ErrTooLarge for an upload that is too
// big, the file is removed and the error returned.
//
// The file is made in dir so that it can be renamed into place there, or
// anywhere on the same filesystem, in one step.
func Receive(dir string, r io.Reader) (*Received, error) {
	f, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hash), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return nil, fmt.Errorf("receiving an upload: %w", err)
	}
	return &Received{Path: f.Name(), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}
