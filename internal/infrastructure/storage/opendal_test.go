package storage

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestOpenDALStorage_Memory(t *testing.T) {
	viper.Set("storage.scheme", "memory")
	defer viper.Set("storage.scheme", "") // reset

	storage, err := NewOpenDALStorage()
	if err != nil {
		t.Fatalf("failed to create memory storage: %v", err)
	}
	defer func() {
		_ = storage.Close()
	}()

	ctx := context.Background()
	key := "test_file.txt"
	data := []byte("hello openDAL memory storage")

	// 1. Test Write
	err = storage.Write(ctx, key, data)
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	// 2. Test Read
	readData, err := storage.Read(ctx, key)
	if err != nil {
		t.Fatalf("failed to read data: %v", err)
	}
	if string(readData) != string(data) {
		t.Errorf("read data mismatch: expected %q, got %q", string(data), string(readData))
	}

	// 3. Test Stat
	meta, err := storage.Stat(ctx, key)
	if err != nil {
		t.Fatalf("failed to get stat: %v", err)
	}
	if meta.ContentLength != int64(len(data)) {
		t.Errorf("expected ContentLength %d, got %d", len(data), meta.ContentLength)
	}
	// Parse LastModified to verify RFC3339 format
	_, err = time.Parse(time.RFC3339, meta.LastModified)
	if err != nil {
		t.Errorf("failed to parse LastModified as RFC3339: %v (value: %s)", err, meta.LastModified)
	}

	// 4. Test Delete
	err = storage.Delete(ctx, key)
	if err != nil {
		t.Fatalf("failed to delete key: %v", err)
	}

	// Verify it's deleted (Read should fail)
	_, err = storage.Read(ctx, key)
	if err == nil {
		t.Error("expected read to fail after delete")
	}
}

func TestNewOpenDALStorage_Invalid(t *testing.T) {
	viper.Set("storage.scheme", "invalid-scheme")
	defer viper.Set("storage.scheme", "")

	_, err := NewOpenDALStorage()
	if err == nil {
		t.Error("expected error for invalid storage scheme")
	}
}
