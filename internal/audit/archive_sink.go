package audit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ArchiveSink abstracts the destination for archive files.
// FileSystemSink is the default implementation; object storage
// backends (S3, GCS) can implement this interface later.
type ArchiveSink interface {
	// CreateTemp creates a temporary writer in the archive directory.
	CreateTemp(dir, pattern string) (io.WriteCloser, string, error)

	// Commit atomically renames a temp file to its final path
	// and writes the checksum sidecar.
	Commit(tempPath, finalPath, checksum string) error
}

// FileSystemSink implements ArchiveSink using the local file system
// with atomic rename semantics.
type FileSystemSink struct{}

// NewFileSystemSink creates a local file system sink.
func NewFileSystemSink() *FileSystemSink { return &FileSystemSink{} }

// CreateTemp creates a temporary file in dir and returns its writer and path.
func (s *FileSystemSink) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, "", fmt.Errorf("create archive dir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	name := f.Name()
	return f, name, nil
}

// Commit fsyncs the temp file, atomically renames it to finalPath,
// and writes a .sha256 sidecar with the checksum.
func (s *FileSystemSink) Commit(tempPath, finalPath, checksum string) error {
	if err := syncPath(tempPath); err != nil {
		return fmt.Errorf("fsync %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tempPath, finalPath, err)
	}
	sidecar := finalPath + ".sha256"
	content := fmt.Sprintf("%s  %s\n", checksum, filepath.Base(finalPath))
	if err := os.WriteFile(sidecar, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write checksum %s: %w", sidecar, err)
	}
	return nil
}

func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
