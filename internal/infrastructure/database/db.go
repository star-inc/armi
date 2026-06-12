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
	ID              string    `gorm:"primaryKey;type:varchar(20)"`
	Filename        string    `gorm:"type:varchar(255)"`
	Description     string    `gorm:"type:text"`
	Hash            string    `gorm:"index;type:varchar(64)"`
	Size            int64     `gorm:"type:bigint"`
	ContentType     string    `gorm:"type:varchar(255)"`
	AuthorID        string    `gorm:"index;type:varchar(20)"`
	Tags            []gormTag `gorm:"many2many:file_tags;"`
	EmbeddingStatus string    `gorm:"type:varchar(50);default:'pending';not null"`
	CreatedAt       time.Time `gorm:"autoCreateTime"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime"`
}

func (gormFileRecord) TableName() string {
	return "file_records"
}

type gormFileGroup struct {
	ID        string    `gorm:"primaryKey;type:varchar(20)"`
	Name      string    `gorm:"type:varchar(255)"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (gormFileGroup) TableName() string {
	return "file_groups"
}

type gormFileGroupMember struct {
	UserID      string    `gorm:"primaryKey;type:varchar(20)"`
	FileGroupID string    `gorm:"primaryKey;column:file_group_id;type:varchar(20)"`
	Permission  int       `gorm:"type:int"`
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}

func (gormFileGroupMember) TableName() string {
	return "file_group_members"
}

type gormFileGroupFile struct {
	FileID      string    `gorm:"primaryKey;column:file_id;type:varchar(20)"`
	FileGroupID string    `gorm:"primaryKey;column:file_group_id;type:varchar(20)"`
	CreatedAt   time.Time `gorm:"autoCreateTime"`
}

func (gormFileGroupFile) TableName() string {
	return "file_group_files"
}

type gormOutboxJob struct {
	ID        string    `gorm:"primaryKey;type:varchar(20)"`
	FileID    string    `gorm:"index;type:varchar(20)"`
	Payload   string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

func (gormOutboxJob) TableName() string {
	return "outbox_jobs"
}

type gormFileHash struct {
	Hash      string    `gorm:"primaryKey;type:varchar(64)"`
	RefCount  int       `gorm:"type:int;default:0"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (gormFileHash) TableName() string {
	return "file_hashes"
}

type gormCleanupJob struct {
	ID             string    `gorm:"primaryKey;type:varchar(20)"`
	FileID         string    `gorm:"index;type:varchar(20)"`
	Hash           string    `gorm:"type:varchar(64)"`
	StorageKey     string    `gorm:"type:varchar(255)"`
	DeletePhysical bool      `gorm:"type:boolean"`
	Status         string    `gorm:"type:varchar(50);default:'pending';not null"`
	RetryCount     int       `gorm:"type:int;default:0"`
	CreatedAt      time.Time `gorm:"autoCreateTime"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime"`
}

func (gormCleanupJob) TableName() string {
	return "cleanup_jobs"
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

	// For SQLite memory database, limit connection pool to 1 connection to prevent isolated databases
	if driver == "sqlite" {
		dbPath := viper.GetString("db.sqlite.path")
		if dbPath == "" {
			dbPath = "armi.db"
		}
		if dbPath == ":memory:" || strings.Contains(dbPath, "mode=memory") {
			sqlDB, err := db.DB()
			if err == nil {
				sqlDB.SetMaxOpenConns(1)
				sqlDB.SetMaxIdleConns(1)
				slog.Info("Configured SQLite connection pool limit to 1 for in-memory DB")
			} else {
				slog.Error("failed to get sql.DB for SQLite pool config", "error", err)
			}
		}
	}

	// Auto migrate the GORM schema models
	err = db.AutoMigrate(
		&gormUser{},
		&gormFileGroup{},
		&gormFileGroupMember{},
		&gormFileGroupFile{},
		&gormFileRecord{},
		&gormTag{},
		&gormOutboxJob{},
		&gormFileHash{},
		&gormCleanupJob{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to auto migrate database: %w", err)
	}

	// If using SQLite and sqlite-vec, create virtual table
	vectorProvider := viper.GetString("vector.provider")
	if vectorProvider == "sqlite-vec" {
		if driver != "sqlite" {
			return nil, fmt.Errorf("sqlite-vec vector provider requires sqlite database driver")
		}

		dimension := viper.GetInt("embedding.dimension")
		if dimension <= 0 {
			dimension = 768
		}
		var createSQL string
		db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'file_embeddings'").Scan(&createSQL)
		expectedEmbeddingColumn := fmt.Sprintf("embedding FLOAT[%d]", dimension)
		needsRecreate := createSQL != "" && (!strings.Contains(createSQL, "+chunk_id") || !strings.Contains(createSQL, expectedEmbeddingColumn))
		if needsRecreate {
			slog.Info("Dropping old file_embeddings table to migrate sqlite-vec schema", "dimension", dimension)
			if dropErr := db.Exec("DROP TABLE file_embeddings").Error; dropErr != nil {
				slog.Error("failed to drop old file_embeddings table", "error", dropErr)
			}
		}

		createTableSQL := fmt.Sprintf(
			"CREATE VIRTUAL TABLE IF NOT EXISTS file_embeddings USING vec0(rowid INTEGER PRIMARY KEY, +chunk_id TEXT, +file_id TEXT, +text TEXT, embedding FLOAT[%d])",
			dimension,
		)
		err = db.Exec(createTableSQL).Error
		if err != nil {
			return nil, fmt.Errorf("failed to create sqlite-vec virtual table: %w", err)
		}
		slog.Info("Initialized sqlite-vec virtual table 'file_embeddings' with chunk support", "dimension", dimension)
	}

	DB = db
	return db, nil
}
