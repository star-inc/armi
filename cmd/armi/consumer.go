package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/star-inc/armi/internal/config"
	"github.com/star-inc/armi/internal/infrastructure/database"
	"github.com/star-inc/armi/internal/infrastructure/embedding"
	"github.com/star-inc/armi/internal/infrastructure/llm"
	"github.com/star-inc/armi/internal/infrastructure/rabbitmq"
	"github.com/star-inc/armi/internal/infrastructure/storage"
	"github.com/star-inc/armi/internal/infrastructure/vector"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newConsumerCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "consumer",
		Short: "Run embedding job consumer",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsumer()
		},
	}
}

func runConsumer() error {
	config.InitConfig()

	// Initialize Database for sqlite-vec, don't use for RDBMS.
	vectorProvider := viper.GetString("vector.provider")
	if vectorProvider == "sqlite-vec" {
		_, err := database.InitDB()
		if err != nil {
			return err
		}
	}
	slog.Info("Vector database initialized successfully", "provider", vectorProvider)

	store, err := storage.NewOpenDALStorage()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			slog.Error("failed to close storage", "error", closeErr)
		}
	}()

	embedder, err := embedding.NewEmbedder()
	if err != nil {
		return err
	}

	vectorDB, err := vector.NewVectorDB()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := vectorDB.Close(); closeErr != nil {
			slog.Error("failed to close vector database", "error", closeErr)
		}
	}()

	llmService, err := llm.NewOpenAILLM()
	if err != nil {
		return err
	}

	publisher, err := rabbitmq.NewRabbitMQPublisher()
	if err != nil {
		slog.Warn("RabbitMQ event publisher initialization failed, event sending will be skipped", "error", err)
	}
	defer func() {
		if publisher != nil {
			if closeErr := publisher.Close(); closeErr != nil {
				slog.Error("failed to close event publisher", "error", closeErr)
			}
		}
	}()

	consumer, err := rabbitmq.NewEmbeddingConsumer(embedder, vectorDB, store, publisher, llmService)
	if err != nil {
		return err
	}
	if consumer == nil {
		slog.Info("Embedding consumer not started because RabbitMQ is disabled")
		return nil
	}
	defer func() {
		if closeErr := consumer.Close(); closeErr != nil {
			slog.Error("failed to close embedding consumer", "error", closeErr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	consumer.Start(ctx)
	return nil
}
