// Package storage stores uploaded files. LocalStore keeps them on the filesystem;
// an S3 implementation can satisfy the same methods later.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned when a key does not exist.
var ErrNotFound = errors.New("file not found")

// LocalStore stores files under a root directory.
type LocalStore struct {
	root string
}

// NewLocalStore returns a LocalStore rooted at dir, creating it when needed.
func NewLocalStore(dir string) (*LocalStore, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve storage dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	return &LocalStore{root: abs}, nil
}

// Put writes data under key atomically (temp file + rename).
func (s *LocalStore) Put(_ context.Context, key string, data []byte) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create dir for %s: %w", key, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", key, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	return nil
}

// Get reads the file stored under key.
func (s *LocalStore) Get(_ context.Context, key string) ([]byte, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", key, err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

// path maps key to a file path, rejecting keys that escape the root.
func (s *LocalStore) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if key == "" || strings.Contains(key, "..") || clean == "/" {
		return "", fmt.Errorf("invalid storage key %q", key)
	}
	return filepath.Join(s.root, clean), nil
}
