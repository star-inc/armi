package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/supersonictw/armi/internal/usecase"
	"github.com/supersonictw/armi/pkgs/contract"
)

// UserHandler handles user-related HTTP endpoints.
type UserHandler struct {
	userUsecase *usecase.UserUsecase
}

// NewUserHandler constructs a new UserHandler.
func NewUserHandler(userUsecase *usecase.UserUsecase) *UserHandler {
	return &UserHandler{userUsecase: userUsecase}
}

// Register registers a new user.
func (h *UserHandler) Register(c *gin.Context) {
	var req contract.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.userUsecase.Register(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}
