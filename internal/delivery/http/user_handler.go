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
// @Summary      Register a new user
// @Description  Register a new user in the system using Argon2id for password hashing.
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        request  body      contract.RegisterRequest  true  "User Registration Details"
// @Success      200      {object}  contract.RegisterResponse
// @Failure      400      {object}  map[string]string "Invalid input or registration error"
// @Router       /users/register [post]
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
