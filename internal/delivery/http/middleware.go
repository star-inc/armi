package http

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/supersonictw/armi/internal/infrastructure/jwtauth"
	"github.com/supersonictw/armi/internal/usecase"
	"github.com/supersonictw/armi/pkgs/file"
)

// AuthMiddleware handles both HTTP Basic Auth and JWT Bearer authentication.
// The accepted scheme is controlled by the jwtauth.AuthScheme parameter:
//   - AuthSchemeBasic  — only Basic Auth is accepted
//   - AuthSchemeBearer — only Bearer JWT is accepted
//   - AuthSchemeBoth   — either scheme is accepted (default)
//
// For Bearer tokens the JWT is validated by the provided Verifier (may be nil
// when scheme is AuthSchemeBasic, in which case Bearer is always rejected).
func AuthMiddleware(
	scheme jwtauth.AuthScheme,
	verifier *jwtauth.Verifier,
	userUsecase *usecase.UserUsecase,
	publisher file.EventPublisher,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		path := c.Request.URL.Path
		method := c.Request.Method

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			slog.Warn("request missing Authorization header", "ip", ip, "path", path)
			publisher.PublishEvent(c.Request.Context(), "user.auth_failed", "", map[string]interface{}{
				"ip":     ip,
				"path":   path,
				"reason": "missing authorization header",
			})
			c.Header("WWW-Authenticate", `Basic realm="armi", Bearer realm="armi"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		switch {
		case strings.HasPrefix(authHeader, "Basic "):
			handleBasicAuth(c, scheme, userUsecase, publisher, ip, path, method, authHeader)

		case strings.HasPrefix(authHeader, "Bearer "):
			handleBearerAuth(c, scheme, verifier, userUsecase, publisher, ip, path, method, authHeader)

		default:
			slog.Warn("unsupported Authorization scheme", "ip", ip, "path", path, "header_prefix", strings.SplitN(authHeader, " ", 2)[0])
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unsupported authorization scheme"})
			return
		}
	}
}

// handleBasicAuth validates HTTP Basic Auth credentials.
func handleBasicAuth(
	c *gin.Context,
	scheme jwtauth.AuthScheme,
	userUsecase *usecase.UserUsecase,
	publisher file.EventPublisher,
	ip, path, method, _ string,
) {
	if scheme == jwtauth.AuthSchemeBearer {
		slog.Warn("Basic Auth rejected: server requires Bearer only", "ip", ip, "path", path)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "basic auth not accepted, use bearer token"})
		return
	}

	username, password, ok := c.Request.BasicAuth()
	if !ok {
		slog.Warn("malformed Basic Auth header", "ip", ip, "path", path)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "malformed basic auth header"})
		return
	}

	dbUser, err := userUsecase.Authenticate(c.Request.Context(), username, password)
	if err != nil {
		slog.Warn("basic auth failed", "username", username, "ip", ip, "path", path, "error", err)
		publisher.PublishEvent(c.Request.Context(), "user.auth_failed", "", map[string]interface{}{
			"username": username,
			"ip":       ip,
			"path":     path,
			"reason":   err.Error(),
		})
		c.Header("WWW-Authenticate", `Basic realm="armi"`)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	c.Set("user", dbUser)
	publisher.PublishEvent(c.Request.Context(), "user.auth_success", dbUser.ID, map[string]interface{}{
		"method":    "basic",
		"username":  dbUser.Username,
		"ip":        ip,
		"path":      path,
		"http_verb": method,
	})
	c.Next()
}

// handleBearerAuth validates a JWT Bearer token and resolves the user by sub claim.
func handleBearerAuth(
	c *gin.Context,
	scheme jwtauth.AuthScheme,
	verifier *jwtauth.Verifier,
	userUsecase *usecase.UserUsecase,
	publisher file.EventPublisher,
	ip, path, method, authHeader string,
) {
	if scheme == jwtauth.AuthSchemeBasic {
		slog.Warn("Bearer token rejected: server requires Basic only", "ip", ip, "path", path)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bearer token not accepted, use basic auth"})
		return
	}

	if verifier == nil {
		slog.Error("Bearer auth attempted but JWT verifier is not configured", "ip", ip, "path", path)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bearer authentication is not configured"})
		return
	}

	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := verifier.Verify(tokenStr)
	if err != nil {
		slog.Warn("bearer token validation failed", "ip", ip, "path", path, "error", err)
		publisher.PublishEvent(c.Request.Context(), "user.auth_failed", "", map[string]interface{}{
			"ip":     ip,
			"path":   path,
			"reason": err.Error(),
		})
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}

	// sub = armi user ID; look up the user to ensure they still exist.
	dbUser, err := userUsecase.GetByID(c.Request.Context(), claims.Subject)
	if err != nil {
		slog.Warn("bearer auth: user not found for sub claim",
			"sub", claims.Subject, "ip", ip, "path", path, "error", err)
		publisher.PublishEvent(c.Request.Context(), "user.auth_failed", "", map[string]interface{}{
			"sub":    claims.Subject,
			"ip":     ip,
			"path":   path,
			"reason": "user not found",
		})
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	c.Set("user", dbUser)
	publisher.PublishEvent(c.Request.Context(), "user.auth_success", dbUser.ID, map[string]interface{}{
		"method":    "bearer",
		"username":  dbUser.Username,
		"ip":        ip,
		"path":      path,
		"http_verb": method,
	})
	c.Next()
}

// FileValidationMiddleware validates that the uploaded file has a valid extension.
// Supported: PDF, Word, PPT, Excel, TXT, RTF
func FileValidationMiddleware(publisher file.EventPublisher) gin.HandlerFunc {
	allowedExts := map[string]bool{
		".pdf":  true,
		".doc":  true,
		".docx": true,
		".xls":  true,
		".xlsx": true,
		".ppt":  true,
		".pptx": true,
		".txt":  true,
		".rtf":  true,
		".md":   true,
	}

	return func(c *gin.Context) {
		// Only check POST requests for multipart form uploads
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing file field"})
			return
		}

		ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
		if !allowedExts[ext] {
			slog.Warn("uploaded file failed format validation", "filename", fileHeader.Filename, "ext", ext)
			publisher.PublishEvent(c.Request.Context(), "file.validation_failed", "", map[string]interface{}{
				"filename":     fileHeader.Filename,
				"content_type": fileHeader.Header.Get("Content-Type"),
				"reason":       "unsupported file format, allowed: PDF, Word, PPT, Excel, TXT, RTF, Markdown",
			})
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "unsupported file format. allowed: .pdf, .doc, .docx, .xls, .xlsx, .ppt, .pptx, .txt, .rtf, .md",
			})
			return
		}

		c.Next()
	}
}
