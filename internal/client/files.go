package client

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/star-inc/armi/pkgs/contract"
)

// ListFilesOptions controls file listing and pagination.
type ListFilesOptions struct {
	Tag      string
	Page     int
	PageSize int
}

// SearchFilesOptions controls semantic file search.
type SearchFilesOptions struct {
	Query        string
	Limit        int
	NLPExpansion bool
	ExpansionNum int
}

// DownloadResult contains downloaded file data and response metadata.
type DownloadResult struct {
	Filename    string
	ContentType string
	Data        []byte
}

// ListFiles returns a paginated list of accessible files.
func (c *Client) ListFiles(ctx context.Context, opts ListFilesOptions) (*contract.FileListResponse, error) {
	if opts.Page <= 0 {
		return nil, fmt.Errorf("page must be a positive integer")
	}
	if opts.PageSize <= 0 || opts.PageSize > 100 {
		return nil, fmt.Errorf("page size must be between 1 and 100")
	}

	query := map[string]string{
		"page":      strconv.Itoa(opts.Page),
		"page_size": strconv.Itoa(opts.PageSize),
	}
	if tag := strings.TrimSpace(opts.Tag); tag != "" {
		query["tag"] = tag
	}

	var result contract.FileListResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetQueryParams(query).
		SetResult(&result).
		Get("/api/v1/files")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}

// DownloadFile downloads a file by ID.
func (c *Client) DownloadFile(ctx context.Context, fileID string) (*DownloadResult, error) {
	resp, err := c.http.R().
		SetContext(ctx).
		SetPathParam("id", fileID).
		Get("/api/v1/files/{id}")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}

	filename := "armi-" + fileID
	if disposition := resp.Header().Get("Content-Disposition"); disposition != "" {
		if _, params, parseErr := mime.ParseMediaType(disposition); parseErr == nil {
			if parsed := filepath.Base(params["filename"]); parsed != "." && parsed != "" {
				filename = parsed
			}
		}
	}

	return &DownloadResult{
		Filename:    filename,
		ContentType: resp.Header().Get("Content-Type"),
		Data:        resp.Body(),
	}, nil
}

// GetFileMetadata returns database and storage metadata for a file.
func (c *Client) GetFileMetadata(ctx context.Context, fileID string) (*contract.FileMetadataResponse, error) {
	var result contract.FileMetadataResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetPathParam("id", fileID).
		SetResult(&result).
		Get("/api/v1/files/{id}/metadata")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}

// UpdateFileMetadata updates mutable metadata fields for a file.
func (c *Client) UpdateFileMetadata(ctx context.Context, fileID string, req contract.UpdateFileMetadataRequest) (*contract.FileResponse, error) {
	body := make(map[string]any)
	if req.Filename != nil {
		body["filename"] = *req.Filename
	}
	if req.Description != nil {
		body["description"] = *req.Description
	}
	if req.GroupIDs != nil {
		body["group_ids"] = req.GroupIDs
	}
	if req.Tags != nil {
		body["tags"] = req.Tags
	}

	var result contract.FileResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetPathParam("id", fileID).
		SetBody(body).
		SetResult(&result).
		Patch("/api/v1/files/{id}/metadata")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}

// DeleteFile deletes a file record and possibly its physical content.
func (c *Client) DeleteFile(ctx context.Context, fileID string) (*contract.DeleteResponse, error) {
	var result contract.DeleteResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetPathParam("id", fileID).
		SetResult(&result).
		Delete("/api/v1/files/{id}")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}

// SearchFiles performs semantic search over accessible files.
func (c *Client) SearchFiles(ctx context.Context, opts SearchFilesOptions) (*contract.SearchListResponse, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if opts.Limit <= 0 {
		return nil, fmt.Errorf("search limit must be a positive integer")
	}
	if opts.ExpansionNum <= 0 {
		return nil, fmt.Errorf("expansion number must be a positive integer")
	}

	var result contract.SearchListResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"q":             query,
			"limit":         strconv.Itoa(opts.Limit),
			"nlp_expansion": strconv.FormatBool(opts.NLPExpansion),
			"expansion_num": strconv.Itoa(opts.ExpansionNum),
		}).
		SetResult(&result).
		Get("/api/v1/files/search")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}
