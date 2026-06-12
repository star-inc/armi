package client

import (
	"context"

	"github.com/star-inc/armi/pkgs/contract"
)

// Health checks whether the Armi server is available.
func (c *Client) Health(ctx context.Context) (*contract.HealthResponse, error) {
	var result contract.HealthResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetResult(&result).
		Get("/healthz")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}
