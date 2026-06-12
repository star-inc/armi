package client

import (
	"context"

	"github.com/star-inc/armi/pkgs/contract"
)

// RegisterUser creates a local Armi user.
func (c *Client) RegisterUser(ctx context.Context, req contract.RegisterRequest) (*contract.RegisterResponse, error) {
	var result contract.RegisterResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&result).
		Post("/api/v1/users/me")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}

// GetMe returns the authenticated user's profile.
func (c *Client) GetMe(ctx context.Context) (*contract.MeResponse, error) {
	var result contract.MeResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetResult(&result).
		Get("/api/v1/users/me")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}

// UpdateMe updates the authenticated user's profile.
func (c *Client) UpdateMe(ctx context.Context, req contract.UpdateMeRequest) (*contract.MeResponse, error) {
	var result contract.MeResponse
	resp, err := c.http.R().
		SetContext(ctx).
		SetBody(req).
		SetResult(&result).
		Patch("/api/v1/users/me")
	if err != nil {
		return nil, err
	}
	if !resp.IsSuccess() {
		return nil, responseError(resp)
	}
	return &result, nil
}
