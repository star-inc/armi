package main

import (
	"context"
	"log"
	"log/slog"

	"github.com/spf13/viper"
	"github.com/supersonictw/armi/internal/config"
	httpDelivery "github.com/supersonictw/armi/internal/delivery/http"
	"github.com/supersonictw/armi/internal/infrastructure/database"
	"github.com/supersonictw/armi/internal/infrastructure/embedding"
	"github.com/supersonictw/armi/internal/infrastructure/jwtauth"
	"github.com/supersonictw/armi/internal/infrastructure/llm"
	"github.com/supersonictw/armi/internal/infrastructure/rabbitmq"
	"github.com/supersonictw/armi/internal/infrastructure/storage"
	"github.com/supersonictw/armi/internal/infrastructure/vector"
	"github.com/supersonictw/armi/internal/usecase"
)

// @title           Armi File Manager API
// @version         1.0
// @description     Armi PDF/Word/Excel/PPT/TXT/RTF 檔案管理器 RESTful API。
// @BasePath        /api/v1
// @securityDefinitions.basic  BasicAuth
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
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

	// 6.6 Initialize and start Embedding Consumer (no-op when RabbitMQ is disabled)
	consumer, err := rabbitmq.NewEmbeddingConsumer(embedder, vectorDB, store, publisher, llmService)
	if err != nil {
		slog.Warn("RabbitMQ embedding consumer initialization failed, falling back to sync embedding", "error", err)
	}
	if consumer != nil {
		consumerCtx, cancelConsumer := context.WithCancel(context.Background())
		defer func() {
			cancelConsumer()
			if closeErr := consumer.Close(); closeErr != nil {
				slog.Error("failed to close embedding consumer", "error", closeErr)
			}
		}()
		go consumer.Start(consumerCtx)
		slog.Info("Embedding consumer goroutine started")
	}

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

	// 10. Instantiate and Run HTTP server (Delivery Layer)
	server := httpDelivery.NewServer(userUsecase, fileUsecase, publisher, authScheme, jwtVerifier)

	host := viper.GetString("HOST")
	port := viper.GetString("PORT")
	addr := host + ":" + port

	if err := server.Run(addr); err != nil {
		log.Fatalf("failed to start HTTP server: %v", err)
	}
}
