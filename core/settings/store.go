package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const MaxConfigBytes = 32 << 10

// FileStore persists the portable settings contract to one local file. It deliberately
// stores only Config, which contains no addresses, SSIDs, endpoints, keys, or tokens.
type FileStore struct {
	path string
}

func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("settings: store path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("settings: store path must be absolute")
	}
	path = filepath.Clean(path)
	if err := validateStoreParent(path); err != nil {
		return nil, err
	}
	return &FileStore{path: path}, nil
}

func (s *FileStore) Load() (Config, error) {
	if s == nil {
		return Config{}, errors.New("settings: nil file store")
	}
	if err := validateStoreParent(s.path); err != nil {
		return Config{}, err
	}
	info, err := os.Lstat(s.path)
	if err != nil {
		return Config{}, fmt.Errorf("settings: lstat store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, errors.New("settings: store must be a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > MaxConfigBytes {
		return Config{}, fmt.Errorf("settings: store size %d outside 1..%d bytes", info.Size(), MaxConfigBytes)
	}

	f, err := os.Open(s.path)
	if err != nil {
		return Config{}, fmt.Errorf("settings: open store: %w", err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("settings: read store: %w", err)
	}
	if len(data) > MaxConfigBytes {
		return Config{}, fmt.Errorf("settings: store exceeds %d bytes", MaxConfigBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("settings: decode store: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return Config{}, errors.New("settings: trailing JSON data")
	}
	return Normalize(cfg)
}

func (s *FileStore) Save(cfg Config) error {
	if s == nil {
		return errors.New("settings: nil file store")
	}
	normalized, err := Normalize(cfg)
	if err != nil {
		return err
	}
	if err := validateStoreParent(s.path); err != nil {
		return err
	}

	if info, err := os.Lstat(s.path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("settings: refusing to replace non-regular store path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("settings: lstat store: %w", err)
	}

	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("settings: encode store: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxConfigBytes {
		return fmt.Errorf("settings: encoded config exceeds %d bytes", MaxConfigBytes)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("settings: create store directory: %w", err)
	}
	if err := validateStoreParent(s.path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bondify-settings-*")
	if err != nil {
		return fmt.Errorf("settings: create temporary store: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("settings: chmod temporary store: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("settings: write temporary store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("settings: sync temporary store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("settings: close temporary store: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("settings: atomically replace store: %w", err)
	}
	committed = true

	if dirFD, err := os.Open(dir); err == nil {
		_ = dirFD.Sync()
		_ = dirFD.Close()
	}
	return nil
}

// validateStoreParent rejects any existing symlink in the directory chain that
// contains the settings file. Missing suffix directories are allowed because
// Save creates them with restrictive permissions and validates the chain again
// before creating the temporary file.
func validateStoreParent(path string) error {
	dir := filepath.Clean(filepath.Dir(path))
	probe := dir
	for {
		info, err := os.Lstat(probe)
		if err == nil {
			if !info.IsDir() {
				return errors.New("settings: store parent must be a directory")
			}
			resolved, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return fmt.Errorf("settings: resolve store parent: %w", err)
			}
			absProbe, err := filepath.Abs(probe)
			if err != nil {
				return fmt.Errorf("settings: resolve store parent path: %w", err)
			}
			absResolved, err := filepath.Abs(resolved)
			if err != nil {
				return fmt.Errorf("settings: resolve store parent target: %w", err)
			}
			rel, err := filepath.Rel(absProbe, absResolved)
			if err != nil || rel != "." {
				return errors.New("settings: symlinked store parent is not allowed")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("settings: stat store parent: %w", err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return errors.New("settings: no existing store parent")
		}
		probe = parent
	}
}
