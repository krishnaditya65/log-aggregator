package coldstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Blobstore is the abstraction cold storage backends implement. Keys are
// slash-separated paths, e.g. "service/2026/01/15/segment-100-200.col".
type Blobstore interface {
	Put(ctx context.Context, key string, data io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]string, error)
}

// FSBlobstore is a Blobstore backed by the local filesystem. It is the
// default backend when no S3 configuration is supplied.
type FSBlobstore struct {
	RootDir string
}

// NewFSBlobstore creates a FSBlobstore rooted at dir, creating it if
// necessary.
func NewFSBlobstore(dir string) (*FSBlobstore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FSBlobstore{RootDir: dir}, nil
}

func (fs *FSBlobstore) resolve(key string) (string, error) {
	clean := filepath.Clean("/" + key) // prevent ../ escapes
	return filepath.Join(fs.RootDir, clean), nil
}

func (fs *FSBlobstore) Put(_ context.Context, key string, data io.Reader) error {
	path, err := fs.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, data)
	return err
}

func (fs *FSBlobstore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := fs.resolve(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (fs *FSBlobstore) List(_ context.Context, prefix string) ([]string, error) {
	root, err := fs.resolve(prefix)
	if err != nil {
		return nil, err
	}
	var keys []string
	// Walk from the deepest directory that certainly exists: if the
	// prefix names a directory, walk it; otherwise walk its parent and
	// filter by the prefix's basename.
	walkRoot := root
	filterName := ""
	if fi, statErr := os.Stat(root); statErr != nil || !fi.IsDir() {
		walkRoot = filepath.Dir(root)
		filterName = filepath.Base(root)
	}

	err = filepath.Walk(walkRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(fs.RootDir, p)
		if relErr != nil {
			return relErr
		}
		key := filepath.ToSlash(rel)
		if filterName != "" && !strings.HasPrefix(filepath.Base(p), filterName) {
			return nil
		}
		if !strings.HasPrefix(key, strings.TrimPrefix(prefix, "/")) {
			return nil
		}
		keys = append(keys, key)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// Delete removes an object. Not part of the Blobstore interface (S3-style
// object stores support it too, but callers that only need eventual
// consistency listing don't need to depend on it) - internal/lifecycle's
// DeleteFunc is wired against this directly for local deployments.
func (fs *FSBlobstore) Delete(_ context.Context, key string) error {
	path, err := fs.resolve(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

var _ Blobstore = (*FSBlobstore)(nil)

// DefaultBlobstore returns a FSBlobstore rooted at dir; used as the
// fallback when no S3Blobstore configuration is provided.
func DefaultBlobstore(dir string) (Blobstore, error) {
	return NewFSBlobstore(dir)
}
