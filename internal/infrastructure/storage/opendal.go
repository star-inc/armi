package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/apache/opendal-go-services/fs"
	"github.com/apache/opendal-go-services/memory"
	opendal "github.com/apache/opendal/bindings/go"
	"github.com/spf13/viper"
	"github.com/star-inc/armi/pkgs/file"
)

// OpenDALStorage implements file.Storage interface using Apache OpenDAL.
type OpenDALStorage struct {
	op *opendal.Operator
}

// NewOpenDALStorage initializes OpenDAL operator based on config.
func NewOpenDALStorage() (file.Storage, error) {
	schemeName := viper.GetString("storage.scheme")
	var scheme opendal.Scheme
	opts := opendal.OperatorOptions{}

	switch schemeName {
	case "fs":
		scheme = fs.Scheme
		root := viper.GetString("storage.root")
		if root == "" {
			root = "./uploads"
		}
		opts["root"] = root
		slog.Info("Initializing OpenDAL filesystem storage", "root", root)
	case "memory":
		scheme = memory.Scheme
		slog.Info("Initializing OpenDAL in-memory storage")
	default:
		return nil, fmt.Errorf("unsupported storage scheme: %s", schemeName)
	}

	op, err := opendal.NewOperator(scheme, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenDAL operator: %w", err)
	}

	return &OpenDALStorage{op: op}, nil
}

// Write saves file data.
func (s *OpenDALStorage) Write(ctx context.Context, key string, data []byte) error {
	err := s.op.Write(key, data)
	if err != nil {
		return err
	}
	return nil
}

// Read retrieves file data.
func (s *OpenDALStorage) Read(ctx context.Context, key string) ([]byte, error) {
	data, err := s.op.Read(key)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Delete removes file data.
func (s *OpenDALStorage) Delete(ctx context.Context, key string) error {
	err := s.op.Delete(key)
	if err != nil {
		return err
	}
	return nil
}

// Stat retrieves file metadata.
func (s *OpenDALStorage) Stat(ctx context.Context, key string) (*file.StorageMetadata, error) {
	stat, err := s.op.Stat(key)
	if err != nil {
		return nil, err
	}
	return &file.StorageMetadata{
		ContentLength: int64(stat.ContentLength()),
		LastModified:  stat.LastModified().Format(time.RFC3339),
	}, nil
}

// Close releases storage operator resources.
func (s *OpenDALStorage) Close() error {
	s.op.Close()
	return nil
}
