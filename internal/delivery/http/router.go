package http

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/supersonictw/armi/internal/usecase"
	"github.com/supersonictw/armi/pkgs/file"
)

// Server wraps the Gin Engine.
type Server struct {
	Engine *gin.Engine
}

// NewServer initializes Gin engine and binds usecases to request handlers.
func NewServer(
	userUsecase *usecase.UserUsecase,
	fileUsecase *usecase.FileUsecase,
	publisher file.EventPublisher,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	s := &Server{
		Engine: r,
	}

	userHandler := NewUserHandler(userUsecase)
	fileHandler := NewFileHandler(fileUsecase, publisher)

	api := r.Group("/api/v1")
	{
		api.POST("/users/register", userHandler.Register)

		auth := api.Group("/")
		auth.Use(BasicAuthMiddleware(userUsecase, publisher))
		{
			auth.POST("/files", FileValidationMiddleware(publisher), fileHandler.Upload)
			auth.GET("/files", fileHandler.List)
			auth.GET("/files/:id", fileHandler.Download)
			auth.GET("/files/:id/metadata", fileHandler.GetMetadata)
			auth.DELETE("/files/:id", fileHandler.Delete)
			auth.GET("/files/search", fileHandler.Search)
		}
	}

	return s
}

// Run starts the HTTP server.
func (s *Server) Run(addr string) error {
	slog.Info("Starting HTTP API server", "addr", addr)
	return s.Engine.Run(addr)
}
