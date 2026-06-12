package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/star-inc/armi/pkgs/contract"
)

// Config controls how the REST client connects to a Armi server.
type Config struct {
	BaseURL     string
	Username    string
	Password    string
	BearerToken string
	Timeout     time.Duration
}

// UploadOptions contains per-upload metadata.
type UploadOptions struct {
	Description string
	Tags        []string
	GroupIDs    []string
}

// Client wraps a resty HTTP client for the Armi API.
type Client struct {
	http *resty.Client
}

// New constructs a configured Armi API client.
func New(cfg Config) (*Client, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, errors.New("base URL is required")
	}

	httpClient := resty.New().
		SetBaseURL(strings.TrimRight(baseURL, "/")).
		SetHeader("Accept", "application/json").
		SetHeader("User-Agent", "armi-client/1.0")

	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	httpClient.SetTimeout(cfg.Timeout)

	username := strings.TrimSpace(cfg.Username)
	password := cfg.Password
	token := strings.TrimSpace(cfg.BearerToken)

	switch {
	case token != "" && username != "":
		return nil, errors.New("use either bearer token or basic auth, not both")
	case token != "":
		httpClient.SetAuthToken(token)
	case username != "":
		httpClient.SetBasicAuth(username, password)
	}

	return &Client{http: httpClient}, nil
}

// UploadFile uploads one local file to POST /api/v1/files.
func (c *Client) UploadFile(ctx context.Context, localPath string, opts UploadOptions) (*contract.FileResponse, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", localPath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%q is a directory", localPath)
	}

	req := c.http.R().SetContext(ctx)
	req.SetFile("file", localPath)
	req.SetFormData(buildFormData(opts))

	var result contract.FileResponse
	resp, err := req.SetResult(&result).Post("/api/v1/files")
	if err != nil {
		return nil, err
	}

	if resp.IsSuccess() {
		return &result, nil
	}

	switch resp.StatusCode() {
	case 409:
		var conflict contract.FileConflictResponse
		if err := json.Unmarshal(resp.Body(), &conflict); err != nil {
			return nil, fmt.Errorf("upload conflict: %s", resp.Status())
		}
		if conflict.Error != "" {
			return nil, fmt.Errorf("%s", conflict.Error)
		}
		return nil, fmt.Errorf("upload conflict: conflicting_id=%s conflicting_hash=%s", conflict.ConflictingID, conflict.ConflictingHash)
	default:
		return nil, responseError(resp)
	}
}

func responseError(resp *resty.Response) error {
	var apiErr contract.ErrorResponse
	if err := json.Unmarshal(resp.Body(), &apiErr); err == nil && apiErr.Error != "" {
		return fmt.Errorf("%s: %s", resp.Status(), apiErr.Error)
	}
	return fmt.Errorf("request failed: %s", resp.Status())
}

func buildFormData(opts UploadOptions) map[string]string {
	form := make(map[string]string)

	if desc := strings.TrimSpace(opts.Description); desc != "" {
		form["description"] = desc
	}
	if tags := cleanJoin(opts.Tags); tags != "" {
		form["tags"] = tags
	}
	if groupIDs := cleanJoin(opts.GroupIDs); groupIDs != "" {
		form["group_ids"] = groupIDs
	}

	return form
}

func cleanJoin(values []string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return strings.Join(out, ",")
}

// NormalizePath returns a clean absolute or relative path suitable for display.
func NormalizePath(root, path string) string {
	if root == "" {
		return filepath.Clean(path)
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(rel)
}
