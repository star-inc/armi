package database

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/spf13/viper"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// gormUser represents the database schema for users.
type gormUser struct {
	ID           string    `gorm:"primaryKey;type:varchar(20)"`
	Username     string    `gorm:"uniqueIndex;type:varchar(255)"`
	PasswordHash string    `gorm:"type:varchar(255)"`
	CreatedAt    time.Time `gorm:"autoCreateTime"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime"`
}

// TableName overrides the table name for gormUser to "users"
func (gormUser) TableName() string {
	return "users"
}

// gormTag represents the database schema for tags.
type gormTag struct {
	ID        string    `gorm:"primaryKey;type:varchar(20)"`
	Name      string    `gorm:"uniqueIndex;type:varchar(100)"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// TableName overrides the table name for gormTag to "tags"
func (gormTag) TableName() string {
	return "tags"
}

// gormFileRecord represents the database schema for file records.
type gormFileRecord struct {
	ID          string    `gorm:"primaryKey;type:varchar(20)"`
	Filename    string    `gorm:"type:varchar(255)"`
	Hash        string    `gorm:"index;type:varchar(64)"`
	Size        int64     `gorm:"type:bigint"`
	ContentType string    `gorm:"type:varchar(255)"`
	OwnerID     string    `gorm:"index;type:varchar(20)"`
	Tags        []gormTag `gorm:"many2many:file_tags;"`
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}

// TableName overrides the table name for gormFileRecord to "file_records"
func (gormFileRecord) TableName() string {
	return "file_records"
}

// DB is the shared GORM database instance.
var DB *gorm.DB

// InitDB initializes GORM (SQLite or Postgres) and runs auto migration.
func InitDB() (*gorm.DB, error) {
	driver := viper.GetString("db.driver")
	var dialector gorm.Dialector

	if driver == "sqlite" {
		sqlite_vec.Auto()
		dbPath := viper.GetString("db.sqlite.path")
		if dbPath == "" {
			dbPath = "armi.db"
		}
		slog.Info("Connecting to SQLite database", "path", dbPath)
		dialector = sqlite.Open(dbPath)
	} else if driver == "postgres" {
		dsn := viper.GetString("db.postgres.dsn")
		slog.Info("Connecting to PostgreSQL database")
		dialector = postgres.Open(dsn)
	} else {
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Auto migrate the GORM schema models
	err = db.AutoMigrate(&gormUser{}, &gormFileRecord{}, &gormTag{})
	if err != nil {
		return nil, fmt.Errorf("failed to auto migrate database: %w", err)
	}

	// If using SQLite and sqlite-vec, create virtual table
	if driver == "sqlite" && viper.GetString("vector.provider") == "sqlite-vec" {
		var createSQL string
		db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'file_embeddings'").Scan(&createSQL)
		if createSQL != "" && !strings.Contains(createSQL, "+chunk_id") {
			slog.Info("Dropping old file_embeddings table to migrate to +column schema")
			if dropErr := db.Exec("DROP TABLE file_embeddings").Error; dropErr != nil {
				slog.Error("failed to drop old file_embeddings table", "error", dropErr)
			}
		}

		err = db.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS file_embeddings USING vec0(rowid INTEGER PRIMARY KEY, +chunk_id TEXT, +file_id TEXT, +text TEXT, embedding FLOAT[768])").Error
		if err != nil {
			return nil, fmt.Errorf("failed to create sqlite-vec virtual table: %w", err)
		}
		slog.Info("Initialized sqlite-vec virtual table 'file_embeddings' with chunk support")
	}

	DB = db
	return db, nil
}
