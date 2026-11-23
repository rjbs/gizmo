package httpd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/rjbs/gizmo/blob"
)

// limiter holds each user to so many things at once.  One more waits its
// turn; it is not turned away.
type limiter struct {
	max  int
	mu   sync.Mutex
	held map[string]chan struct{}
}

func newLimiter(max int) *limiter {
	return &limiter{max: max, held: map[string]chan struct{}{}}
}

// acquire waits for one of the user's places, and returns the function that
// gives it back, or false if ctx is done first.
func (l *limiter) acquire(ctx context.Context, user string) (func(), bool) {
	l.mu.Lock()
	slots, ok := l.held[user]
	if !ok {
		slots = make(chan struct{}, l.max)
		l.held[user] = slots
	}
	l.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	case <-ctx.Done():
		return nil, false
	}
}

// capped reads from r until more than limit bytes have come, and from then
// on gives blob.ErrTooLarge.
type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, blob.ErrTooLarge
	}
	// One byte more than is allowed is asked for, to tell an upload of
	// exactly the limit from one that goes past it.
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, blob.ErrTooLarge
	}
	return n, err
}

// handleUpload is the upload endpoint (RFC 8620 §6.1).
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("accountId")
	release, ok := s.uploads.acquire(r.Context(), user(r.Context()).ID)
	if !ok {
		return
	}
	defer release()

	limit := s.cfg.Limits.MaxSizeUpload
	if r.ContentLength > limit {
		http.Error(w, "the upload is larger than maxSizeUpload", http.StatusRequestEntityTooLarge)
		return
	}
	up, err := s.cfg.Blobs.Put(r.Context(), &blob.Upload{
		AccountID: accountID,
		Type:      r.Header.Get("Content-Type"),
		Body:      &capped{r: r.Body, left: limit},
	})
	var refusal *blob.Refusal
	switch {
	case errors.Is(err, blob.ErrTooLarge):
		http.Error(w, "the upload is larger than maxSizeUpload", http.StatusRequestEntityTooLarge)
		return
	case errors.As(err, &refusal):
		http.Error(w, refusal.Detail, refusal.Status)
		return
	case err != nil:
		s.cfg.Logger.Error("storing an upload", "account", accountID, "error", err)
		http.Error(w, "could not store the upload", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"accountId": accountID,
		"blobId":    up.BlobID,
		"type":      up.Type,
		"size":      up.Size,
	})
}

// handleDownload is the download endpoint (RFC 8620 §6.2).  A blob id
// always names the same bytes, so what is sent may be kept for good; range
// requests are http.ServeContent's doing.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	accountID, blobID := r.PathValue("accountId"), r.PathValue("blobId")
	b, err := s.cfg.Blobs.Get(r.Context(), accountID, blobID)
	if errors.Is(err, blob.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.cfg.Logger.Error("opening a blob", "account", accountID, "blob", blobID, "error", err)
		http.Error(w, "could not read the blob", http.StatusInternalServerError)
		return
	}
	if c, ok := b.Content.(io.Closer); ok {
		defer c.Close()
	}
	contentType := b.Type
	if t := r.URL.Query().Get("type"); t != "" {
		contentType = t
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, immutable, max-age=31536000")
	http.ServeContent(w, r, r.PathValue("name"), b.ModTime, b.Content)
}
