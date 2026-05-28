package config

import (
	"log/slog"
	"strings"

	"github.com/spf13/viper"
)

// InitConfig initializes the Viper configuration system.
// It loads settings from .env or config.toml, and binds OS environment variables.
func InitConfig() {
	// Try loading from .env first
	viper.SetConfigFile(".env")
	viper.SetConfigType("env")

	if err := viper.ReadInConfig(); err != nil {
		// Fallback to config.toml if .env is missing or has error
		viper.SetConfigFile("config.toml")
		viper.SetConfigType("toml")
		if err := viper.ReadInConfig(); err != nil {
			slog.Info("No configuration file (.env or config.toml) found, relying on system environment variables and defaults")
		} else {
			slog.Info("Loaded configuration from config.toml")
		}
	} else {
		slog.Info("Loaded configuration from .env")
	}

	// AutomaticEnv binds env variables
	viper.AutomaticEnv()

	// Use a replacer to allow environment variables with underscores (e.g. DB_DRIVER)
	// to match nested config keys (e.g. db.driver)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Set Default Values
	viper.SetDefault("PORT", "8080")
	viper.SetDefault("HOST", "0.0.0.0")

	// RDBMS Defaults
	viper.SetDefault("db.driver", "sqlite")
	viper.SetDefault("db.sqlite.path", "armi.db")
	viper.SetDefault("db.postgres.dsn", "host=localhost user=postgres password=postgres dbname=armi port=5432 sslmode=disable")

	// Storage Defaults
	viper.SetDefault("storage.scheme", "fs")
	viper.SetDefault("storage.root", "./uploads")

	// Embedding Defaults
	viper.SetDefault("embedding.provider", "ollama")
	viper.SetDefault("embedding.model", "nomic-embed-text-v2")
	viper.SetDefault("embedding.ollama.base_url", "http://localhost:11434")
	viper.SetDefault("embedding.openai.api_key", "")
	viper.SetDefault("embedding.openai.base_url", "https://api.openai.com/v1")

	// VectorDB Defaults
	viper.SetDefault("vector.provider", "sqlite-vec")
	viper.SetDefault("vector.qdrant.url", "http://localhost:6333")
	viper.SetDefault("vector.qdrant.collection", "armi_files")

	// RabbitMQ Defaults
	viper.SetDefault("rabbitmq.enabled", false)
	viper.SetDefault("rabbitmq.url", "amqp://guest:guest@localhost:5672/")
	viper.SetDefault("rabbitmq.exchange", "armi.events")
	viper.SetDefault("rabbitmq.routing_key", "user.events")
	viper.SetDefault("rabbitmq.broadcast_exchange", "armi.events.broadcast") // fanout, for progress events
	viper.SetDefault("rabbitmq.embedding_queue", "armi.embedding.jobs")      // durable work queue


	// NLP Search and LLM Defaults
	viper.SetDefault("search.nlp_expansion.enabled", true)
	viper.SetDefault("search.nlp_expansion.max_limit", 10)
	viper.SetDefault("llm.model", "gpt-4o-mini")
	viper.SetDefault("llm.openai.api_key", "")
	viper.SetDefault("llm.openai.base_url", "https://api.openai.com/v1")
}
