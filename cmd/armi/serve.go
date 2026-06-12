package main

import (
	"context"
	"log"
	"log/slog"

	"github.com/star-inc/armi/internal/config"
	httpDelivery "github.com/star-inc/armi/internal/delivery/http"
	"github.com/star-inc/armi/internal/infrastructure/database"
	"github.com/star-inc/armi/internal/infrastructure/embedding"
	"github.com/star-inc/armi/internal/infrastructure/jwtauth"
	"github.com/star-inc/armi/internal/infrastructure/llm"
	"github.com/star-inc/armi/internal/infrastructure/rabbitmq"
	"github.com/star-inc/armi/internal/infrastructure/storage"
	"github.com/star-inc/armi/internal/infrastructure/vector"
	"github.com/star-inc/armi/internal/usecase"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run HTTP API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe()
		},
	}
}

func runServe() error {
	// 1. Initialize Configuration (Viper)
	config.InitConfig()

	// 2. Initialize Database (GORM RDBMS)
	db, err := database.InitDB()
	if err != nil {
		log.Fatalf("failed to initialize database: %v", err)
	}
	slog.Info("Database initialized successfully", "driver", viper.GetString("db.driver"))

	// 3. Initialize OpenDAL Storage
	store, err := storage.NewOpenDALStorage()
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}
	defer func() {
		if store != nil {
			if closeErr := store.Close(); closeErr != nil {
				slog.Error("failed to close storage", "error", closeErr)
			}
		}
	}()

	// 4. Initialize Embedding Embedder
	embedder, err := embedding.NewEmbedder()
	if err != nil {
		log.Fatalf("failed to initialize embedding: %v", err)
	}

	// 5. Initialize Vector Database
	vectorDB, err := vector.NewVectorDB()
	if err != nil {
		log.Fatalf("failed to initialize vector database: %v", err)
	}
	defer func() {
		if vectorDB != nil {
			if closeErr := vectorDB.Close(); closeErr != nil {
				slog.Error("failed to close vector database", "error", closeErr)
			}
		}
	}()

	// 5.5 Initialize LLM Service (OpenAI)
	llmService, err := llm.NewOpenAILLM()
	if err != nil {
		log.Fatalf("failed to initialize LLM: %v", err)
	}

	// 6. Initialize RabbitMQ Event Publisher
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

	eventHub := httpDelivery.NewEventsHub()

	// 6.5 Initialize RabbitMQ Embedding Job Publisher
	// When RabbitMQ is unavailable, jobPublisher.IsAvailable() returns false and
	// the upload usecase falls back to synchronous embedding automatically.
	jobPublisher, err := rabbitmq.NewRabbitMQJobPublisher()
	if err != nil {
		slog.Warn("RabbitMQ job publisher initialization failed, upload will embed synchronously", "error", err)
	}
	defer func() {
		if jobPublisher != nil {
			if closeErr := jobPublisher.Close(); closeErr != nil {
				slog.Error("failed to close job publisher", "error", closeErr)
			}
		}
	}()

	// 7. Instantiate Repositories
	userRepo := database.NewGormUserRepository(db)
	fileRepo := database.NewGormFileRepository(db)

	// 8. Instantiate Use Cases (Business Logic Layers)
	userUsecase := usecase.NewUserUsecase(userRepo, publisher)
	fileUsecase := usecase.NewFileUsecase(fileRepo, store, embedder, vectorDB, llmService, publisher, jobPublisher)

	// 9. Initialize JWT Verifier (optional — skipped when jwt.issuer is not configured)
	authScheme := jwtauth.ParseAuthScheme(viper.GetString("auth.scheme"))
	var jwtVerifier *jwtauth.Verifier

	if viper.GetString("jwt.issuer") != "" {
		algStrs := viper.GetStringSlice("jwt.algorithms")
		var algorithms []jwtauth.Algorithm
		for _, algStr := range algStrs {
			alg, err := jwtauth.ParseAlgorithm(algStr)
			if err != nil {
				log.Fatalf("invalid JWT algorithm in config: %v", err)
			}
			algorithms = append(algorithms, alg)
		}

		verifier, err := jwtauth.NewVerifier(jwtauth.Config{
			Algorithms:        algorithms,
			Issuer:            viper.GetString("jwt.issuer"),
			Audience:          viper.GetString("jwt.audience"),
			HS256Secret:       viper.GetString("jwt.hs256.secret"),
			RS256PublicKeyPEM: viper.GetString("jwt.rs256.public_key_pem"),
			ES256PublicKeyPEM: viper.GetString("jwt.es256.public_key_pem"),
		})
		if err != nil {
			log.Fatalf("failed to initialize JWT verifier: %v", err)
		}
		jwtVerifier = verifier
	} else {
		slog.Info("JWT not configured (jwt.issuer is empty), Bearer auth disabled")
		if authScheme == jwtauth.AuthSchemeBearer {
			log.Fatal("auth.scheme is 'bearer' but jwt.issuer is not configured")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventBroadcaster, err := rabbitmq.NewEventBroadcaster(eventHub)
	if err != nil {
		slog.Warn("Failed to initialize event broadcaster, SSE events will be unavailable", "error", err)
	} else if eventBroadcaster != nil {
		go eventBroadcaster.Start(ctx)
		defer func() {
			if closeErr := eventBroadcaster.Close(); closeErr != nil {
				slog.Error("failed to close event broadcaster", "error", closeErr)
			}
		}()
	}

	eventConsumer, err := rabbitmq.NewEventConsumer(fileRepo)
	if err != nil {
		slog.Warn("Failed to initialize event consumer, DB updates on async events will be skipped", "error", err)
	} else if eventConsumer != nil {
		go eventConsumer.Start(ctx)
		defer func() {
			if closeErr := eventConsumer.Close(); closeErr != nil {
				slog.Error("failed to close event consumer", "error", closeErr)
			}
		}()
	}

	// 9.5 Initialize and start background dispatcher and cleanup saga workers
	if jobPublisher != nil {
		dispatcher := usecase.NewOutboxDispatcher(fileRepo, jobPublisher)
		go dispatcher.Start(ctx)
	}
	cleanupWorker := usecase.NewCleanupWorker(fileRepo, vectorDB, store)
	go cleanupWorker.Start(ctx)

	// 10. Instantiate and Run HTTP server (Delivery Layer)
	server := httpDelivery.NewServer(userUsecase, fileUsecase, publisher, authScheme, jwtVerifier, eventHub)

	host := viper.GetString("HOST")
	port := viper.GetString("PORT")
	addr := host + ":" + port

	return server.Run(addr)
}
