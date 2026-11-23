package bakery

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/rjbs/gizmo/blob"
)

// pantry is the bakery's blobs: files in a directory, each named for the
// account it belongs to and the digest of its bytes.  An upload is written
// to disk as it arrives and never held in memory.
type pantry struct {
	dir string

	mu    sync.Mutex
	types map[string]string // by file name

	// gate, if set, is waited on by each upload before it begins, so that
	// a test can hold uploads where they stand.  entered counts them.
	gate    chan struct{}
	entered chan struct{}
}

func newPantry() *pantry {
	dir, err := os.MkdirTemp("", "bakery-")
	if err != nil {
		panic(err)
	}
	return &pantry{dir: dir, types: map[string]string{}}
}

func (p *pantry) file(accountID, blobID string) string {
	return filepath.Join(p.dir, accountID+"-"+blobID)
}

func (p *pantry) Put(_ context.Context, up *blob.Upload) (*blob.Uploaded, error) {
	if p.gate != nil {
		p.entered <- struct{}{}
		<-p.gate
	}
	// The bakery won't keep anything that says it is poison.
	if up.Type == "application/x-poison" {
		return nil, &blob.Refusal{Status: 415, Detail: "the bakery keeps no poison"}
	}
	got, err := blob.Receive(p.dir, up.Body)
	if err != nil {
		return nil, err
	}
	id := "B" + got.SHA256[:16]
	if err := os.Rename(got.Path, p.file(up.AccountID, id)); err != nil {
		os.Remove(got.Path)
		return nil, err
	}
	p.mu.Lock()
	p.types[up.AccountID+"-"+id] = up.Type
	p.mu.Unlock()
	return &blob.Uploaded{BlobID: id, Type: up.Type, Size: got.Size}, nil
}

func (p *pantry) Get(_ context.Context, accountID, blobID string) (*blob.Blob, error) {
	p.mu.Lock()
	contentType, ok := p.types[accountID+"-"+blobID]
	p.mu.Unlock()
	if !ok {
		return nil, blob.ErrNotFound
	}
	f, err := os.Open(p.file(accountID, blobID))
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &blob.Blob{Content: f, Type: contentType, ModTime: info.ModTime()}, nil
}

// Files lists the names of the files in the pantry, which a test may use to
// see that nothing was left behind.
func (b *Bakery) Files() []string {
	entries, _ := os.ReadDir(b.pantry.dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// HoldUploads makes every upload from now on wait, once it has arrived,
// until the function returned is called.  The channel returned receives
// once for each upload that arrives.
func (b *Bakery) HoldUploads() (arrived <-chan struct{}, release func()) {
	b.pantry.gate = make(chan struct{})
	b.pantry.entered = make(chan struct{}, 64)
	return b.pantry.entered, func() { close(b.pantry.gate) }
}

// Close removes the bakery's files.
func (b *Bakery) Close() { os.RemoveAll(b.pantry.dir) }
