package http

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/supersonictw/armi/internal/usecase"
	"github.com/supersonictw/armi/pkgs/file"
)

// BasicAuthMiddleware verifies basic authentication using the UserUsecase.
func BasicAuthMiddleware(userUsecase *usecase.UserUsecase, publisher file.EventPublisher) gin.HandlerFunc {
	return func(c *gin.Context) {
		username, password, hasAuth := c.Request.BasicAuth()
		ip := c.ClientIP()
		path := c.Request.URL.Path
		method := c.Request.Method

		if !hasAuth {
			slog.Warn("request missing Authorization header", "ip", ip, "path", path)
			publisher.PublishEvent(c.Request.Context(), "user.auth_failed", "", map[string]interface{}{
				"username": "",
				"ip":       ip,
				"path":     path,
				"reason":   "missing authorization header",
			})
			c.Header("WWW-Authenticate", `Basic realm="armi"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		dbUser, err := userUsecase.Authenticate(c.Request.Context(), username, password)
		if err != nil {
			slog.Warn("auth failed", "username", username, "ip", ip, "path", path, "error", err)
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

		// Set the domain User entity in gin context
		c.Set("user", dbUser)

		publisher.PublishEvent(c.Request.Context(), "user.auth_success", dbUser.ID, map[string]interface{}{
			"username": dbUser.Username,
			"ip":       ip,
			"path":     path,
			"method":   method,
		})

		c.Next()
	}
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
				"reason":       "unsupported file format, allowed: PDF, Word, PPT, Excel, TXT, RTF",
			})
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "unsupported file format. allowed: .pdf, .doc, .docx, .xls, .xlsx, .ppt, .pptx, .txt, .rtf",
			})
			return
		}

		c.Next()
	}
}
