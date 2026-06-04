// Package config provides configuration loading and validation for the
// HistorySync Cloud Server, supporting YAML files and environment variable
// overrides via viper.
//
// The OptionStore interface and its default file-backed implementation let
// an operator change select settings at runtime (through admin endpoints)
// without restarting the server. Enterprise deployments can replace the
// store with a database-backed implementation via provider registration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// OptionStore provides read/write access to runtime-configurable key-value
// pairs. The CE default implementation is an in-memory map backed by a YAML
// file; Enterprise can inject a database-backed store.
type OptionStore interface {
	// Get returns the value for key. When the key is not set the second return
	// value is false.
	Get(key string) (string, bool)

	// Set writes a value for key. An empty value is allowed. This is an
	// upsert: it creates the key if absent, updates if present.
	Set(key, value string) error

	// All returns a copy of every key-value pair currently stored.
	All() map[string]string
}

// FileOptionStore is the default CE OptionStore. It holds all options in
// memory and flushes writes to a YAML file so settings survive restarts. It
// is safe for concurrent use.
//
// The file is a flat key: value YAML map (no nesting). It is read at
// construction and overwritten on every Set.
type FileOptionStore struct {
	mu   sync.RWMutex
	data map[string]string
	path string
}

// NewFileOptionStore creates an OptionStore backed by path. When the file
// does not exist the store starts empty and creates it on the first Set.
func NewFileOptionStore(path string) (*FileOptionStore, error) {
	s := &FileOptionStore{
		data: make(map[string]string),
		path: path,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileOptionStore) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

func (s *FileOptionStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return s.flush()
}

func (s *FileOptionStore) All() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// load reads the backing file into the in-memory map. A missing file is
// treated as an empty store (not an error).
func (s *FileOptionStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read option file %s: %w", s.path, err)
	}
	// Parse a minimal flat YAML: one "key: value" per line. This avoids
	// pulling in a YAML library for a trivial format. We keep the store
	// format simple on purpose -- complex nesting belongs in the main config.
	for i, line := range splitLines(string(b)) {
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		idx := findColon(line)
		if idx < 0 {
			return fmt.Errorf("option file %s:%d: expected 'key: value' format", s.path, i+1)
		}
		key := trimSpace(line[:idx])
		val := trimSpace(line[idx+1:])
		if key == "" {
			continue
		}
		s.data[key] = val
	}
	return nil
}

// flush writes the in-memory map to the backing file as a flat YAML map.
func (s *FileOptionStore) flush() error {
	var b []byte
	for k, v := range s.data {
		b = append(b, k+": "+v+"\n"...)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create option file directory: %w", err)
	}
	if err := os.WriteFile(s.path, b, 0o644); err != nil {
		return fmt.Errorf("write option file %s: %w", s.path, err)
	}
	return nil
}

// splitLines splits s into lines, handling both \n and \r\n.
func splitLines(s string) []string {
	var lines []string
	i := 0
	for j := 0; j < len(s); j++ {
		if s[j] == '\n' {
			end := j
			if end > i && s[end-1] == '\r' {
				end--
			}
			lines = append(lines, s[i:end])
			i = j + 1
		}
	}
	if i < len(s) {
		lines = append(lines, s[i:])
	}
	return lines
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func findColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
