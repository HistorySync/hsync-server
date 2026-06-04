package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileOptionStoreSetAndGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "options.yaml")
	s, err := NewFileOptionStore(path)
	if err != nil {
		t.Fatalf("NewFileOptionStore: %v", err)
	}

	_, ok := s.Get("key1")
	if ok {
		t.Fatal("Get for missing key returned ok=true")
	}

	if err := s.Set("key1", "value1"); err != nil {
		t.Fatalf("Set key1: %v", err)
	}
	v, ok := s.Get("key1")
	if !ok || v != "value1" {
		t.Fatalf("Get key1 = (%q, %v), want (value1, true)", v, ok)
	}

	// Update an existing key.
	if err := s.Set("key1", "updated"); err != nil {
		t.Fatalf("Set key1 update: %v", err)
	}
	v, ok = s.Get("key1")
	if !ok || v != "updated" {
		t.Fatalf("Get key1 after update = (%q, %v), want (updated, true)", v, ok)
	}
}

func TestFileOptionStoreAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "options.yaml")
	s, err := NewFileOptionStore(path)
	if err != nil {
		t.Fatalf("NewFileOptionStore: %v", err)
	}

	// Empty store returns empty map.
	if m := s.All(); len(m) != 0 {
		t.Fatalf("All on empty = %+v, want empty map", m)
	}

	s.Set("a", "1")
	s.Set("b", "2")
	all := s.All()
	if len(all) != 2 || all["a"] != "1" || all["b"] != "2" {
		t.Fatalf("All = %+v, want {a:1, b:2}", all)
	}
}

func TestFileOptionStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "options.yaml")

	// First store: write values.
	s1, err := NewFileOptionStore(path)
	if err != nil {
		t.Fatalf("NewFileOptionStore: %v", err)
	}
	s1.Set("persist", "yes")
	s1.Set("other", "val")

	// Second store: read back from the same file.
	s2, err := NewFileOptionStore(path)
	if err != nil {
		t.Fatalf("NewFileOptionStore (reload): %v", err)
	}
	v, ok := s2.Get("persist")
	if !ok || v != "yes" {
		t.Fatalf("Get persist after reload = (%q, %v), want (yes, true)", v, ok)
	}
	v2, ok := s2.Get("other")
	if !ok || v2 != "val" {
		t.Fatalf("Get other after reload = (%q, %v), want (val, true)", v2, ok)
	}
}

func TestFileOptionStoreEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileOptionStore(path)
	if err != nil {
		t.Fatalf("NewFileOptionStore: %v", err)
	}
	if m := s.All(); len(m) != 0 {
		t.Fatalf("All = %+v, want empty map", m)
	}
}

func TestFileOptionStoreCommentsAndBlanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comments.yaml")
	content := "# this is a comment\nkey1: value1\n\n# another comment\nkey2: value2\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileOptionStore(path)
	if err != nil {
		t.Fatalf("NewFileOptionStore: %v", err)
	}
	v, ok := s.Get("key1")
	if !ok || v != "value1" {
		t.Fatalf("Get key1 = (%q, %v), want (value1, true)", v, ok)
	}
	v2, ok := s.Get("key2")
	if !ok || v2 != "value2" {
		t.Fatalf("Get key2 = (%q, %v), want (value2, true)", v2, ok)
	}
}

func TestFileOptionStoreNewFile(t *testing.T) {
	// A path that doesn't exist yet should not error; it's created on first Set.
	path := filepath.Join(t.TempDir(), "does", "not", "exist", "options.yaml")
	s, err := NewFileOptionStore(path)
	if err != nil {
		t.Fatalf("NewFileOptionStore: %v", err)
	}
	if err := s.Set("key", "val"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// File should exist now
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created after Set: %v", err)
	}
}
