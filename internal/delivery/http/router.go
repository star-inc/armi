package http

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "github.com/star-inc/armi/docs"
	"github.com/star-inc/armi/internal/infrastructure/jwtauth"
	"github.com/star-inc/armi/internal/usecase"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
)

// Server wraps the Gin Engine.
type Server struct {
	Engine   *gin.Engine
	EventHub *EventsHub
}

// NewServer initializes Gin engine and binds usecases to request handlers.
func NewServer(
	userUsecase *usecase.UserUsecase,
	fileUsecase *usecase.FileUsecase,
	publisher file.EventPublisher,
	authScheme jwtauth.AuthScheme,
	jwtVerifier *jwtauth.Verifier, // nil when JWT is disabled (Basic-only mode)
	eventHub *EventsHub,
) *Server {
	// gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// Health check endpoint for load balancer
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, contract.HealthResponse{Status: "ok"})
	})

	// Swagger UI endpoint
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	s := &Server{
		Engine:   r,
		EventHub: eventHub,
	}
	if s.EventHub == nil {
		s.EventHub = NewEventsHub()
	}

	userHandler := NewUserHandler(userUsecase)
	fileHandler := NewFileHandler(fileUsecase, publisher)
	mcpHandler := NewMCPHandler(fileUsecase)
	eventsHandler := NewEventsHandler(s.EventHub)

	authOnly := r.Group("/")
	authOnly.Use(AuthMiddleware(authScheme, jwtVerifier, userUsecase, publisher))
	{
		authOnly.GET("/events", eventsHandler.Stream)
	}

	api := r.Group("/api/v1")
	{
		// In Bearer-only mode, local user management endpoints are disabled.
		if authScheme != jwtauth.AuthSchemeBearer {
			api.POST("/users/me", userHandler.CreateMe)
		}

		auth := api.Group("/")
		auth.Use(AuthMiddleware(authScheme, jwtVerifier, userUsecase, publisher))
		{
			auth.GET("/events", eventsHandler.Stream)

			if authScheme != jwtauth.AuthSchemeBearer {
				auth.GET("/users/me", userHandler.GetMe)
				auth.PATCH("/users/me", userHandler.PatchMe)
			}

			auth.POST("/files", FileValidationMiddleware(publisher), fileHandler.Upload)
			auth.GET("/files", fileHandler.List)
			auth.GET("/files/:id", fileHandler.Download)
			auth.GET("/files/:id/metadata", fileHandler.GetMetadata)
			auth.PATCH("/files/:id/metadata", fileHandler.PatchMetadata)
			auth.DELETE("/files/:id", fileHandler.Delete)
			auth.GET("/files/search", fileHandler.Search)

			// MCP (Model Context Protocol) Streamable HTTP endpoint
			auth.Any("/mcp", MCPContextMiddleware(), mcpHandler.StreamableHTTP)
		}
	}

	return s
}

// Run starts the HTTP server.
func (s *Server) Run(addr string) error {
	slog.Info("Starting HTTP API server", "addr", addr)
	return s.Engine.Run(addr)
}
