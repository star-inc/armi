package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/star-inc/armi/internal/usecase"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/user"
)

// UserHandler handles user-related HTTP endpoints.
type UserHandler struct {
	userUsecase *usecase.UserUsecase
}

// NewUserHandler constructs a new UserHandler.
func NewUserHandler(userUsecase *usecase.UserUsecase) *UserHandler {
	return &UserHandler{userUsecase: userUsecase}
}

// CreateMe registers a new user.
// @Summary      Register a new user
// @Description  Register a new user in the system using Argon2id for password hashing.
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        request  body      contract.RegisterRequest  true  "User Registration Details"
// @Success      200      {object}  contract.RegisterResponse
// @Failure      400      {object}  contract.ErrorResponse "Invalid input or registration error"
// @Router       /users/me [post]
func (h *UserHandler) CreateMe(c *gin.Context) {
	var req contract.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: err.Error()})
		return
	}

	resp, err := h.userUsecase.Register(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetMe returns current authenticated user profile.
// @Summary      Get current user profile
// @Tags         users
// @Produce      json
// @Success      200      {object}  contract.MeResponse
// @Failure      401      {object}  contract.ErrorResponse "Unauthorized"
// @Failure      404      {object}  contract.ErrorResponse "User not found"
// @Security     BasicAuth
// @Router       /users/me [get]
func (h *UserHandler) GetMe(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	resp, err := h.userUsecase.GetProfile(c.Request.Context(), dbUser.ID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, contract.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// PatchMe updates current authenticated user profile.
// @Summary      Update current user profile
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        request  body      contract.UpdateMeRequest  true  "Current user profile updates"
// @Success      200      {object}  contract.MeResponse
// @Failure      400      {object}  contract.ErrorResponse "Invalid input"
// @Failure      401      {object}  contract.ErrorResponse "Unauthorized"
// @Failure      500      {object}  contract.ErrorResponse "Update failed"
// @Security     BasicAuth
// @Router       /users/me [patch]
func (h *UserHandler) PatchMe(c *gin.Context) {
	val, ok := c.Get("user")
	if !ok {
		c.JSON(http.StatusUnauthorized, contract.ErrorResponse{Error: "unauthorized"})
		return
	}
	dbUser := val.(*user.User)

	var req contract.UpdateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: err.Error()})
		return
	}
	if req.Username == nil && req.Password == nil {
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: "at least one field is required"})
		return
	}

	resp, err := h.userUsecase.UpdateProfile(c.Request.Context(), dbUser.ID, req.Username, req.Password)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, contract.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, contract.ErrorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}
