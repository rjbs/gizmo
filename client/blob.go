package client

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
)

// Uploaded is what the server says of a blob it was sent.
type Uploaded struct {
	AccountID string `json:"accountId"`
	BlobID    string `json:"blobId"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
}

// expand fills in a URL template of the kind a session has, in which
// "{name}" stands for a value.
func expand(template string, values map[string]string) string {
	for name, value := range values {
		template = strings.ReplaceAll(template, "{"+name+"}", url.PathEscape(value))
	}
	return template
}

// Upload sends content to the client's account as a blob of the given
// type.  The size is the content's length in bytes, or -1 if that isn't
// known, in which case it is sent in chunks.
func (c *Client) Upload(ctx context.Context, contentType string, content io.Reader, size int64) (*Uploaded, error) {
	s, err := c.Session(ctx)
	if err != nil {
		return nil, err
	}
	if s.UploadURL == "" {
		return nil, errors.New("the server takes no uploads")
	}
	if size < 0 {
		// A reader that is only a reader keeps the HTTP client from
		// working out a length of its own.
		content = struct{ io.Reader }{content}
	}
	up := &Uploaded{}
	target := expand(s.UploadURL, map[string]string{"accountId": c.AccountID})
	if err := c.sendJSON(ctx, "POST", target, contentType, content, size, up); err != nil {
		return nil, err
	}
	if up.BlobID == "" {
		return nil, errors.New("the server gave the upload no blobId")
	}
	return up, nil
}

// Download fetches a blob from the client's account.  The name is what the
// server is asked to call the file, and the content type is what it is
// asked to say the blob is; either may matter to the server or not.  The
// caller closes what is returned.
func (c *Client) Download(ctx context.Context, blobID, name, contentType string) (io.ReadCloser, error) {
	s, err := c.Session(ctx)
	if err != nil {
		return nil, err
	}
	if s.DownloadURL == "" {
		return nil, errors.New("the server gives no downloads")
	}
	target := expand(s.DownloadURL, map[string]string{
		"accountId": c.AccountID, "blobId": blobID, "name": name, "type": contentType,
	})
	res, err := c.send(ctx, "GET", target, "", nil, -1)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}
