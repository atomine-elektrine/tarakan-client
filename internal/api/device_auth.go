package api

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	ErrAuthorizationPending = errors.New("browser authorization is still pending")
	ErrAccessDenied         = errors.New("browser authorization was denied")
	ErrDeviceCodeExpired    = errors.New("browser authorization expired")
)

type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

type DeviceCredential struct {
	Token     string    `json:"token"`
	TokenType string    `json:"token_type"`
	ExpiresAt time.Time `json:"expires_at"`
	Scopes    []string  `json:"scopes"`
}

func (c *Client) StartDeviceAuthorization(ctx context.Context, clientName string) (DeviceAuthorization, error) {
	var authorization DeviceAuthorization
	err := c.do(ctx, http.MethodPost, "/api/client-auth/start", map[string]string{
		"client_name": clientName,
	}, &authorization)
	return authorization, err
}

func (c *Client) ExchangeDeviceAuthorization(ctx context.Context, deviceCode string) (DeviceCredential, error) {
	var credential DeviceCredential
	err := c.do(ctx, http.MethodPost, "/api/client-auth/exchange", map[string]string{
		"device_code": deviceCode,
	}, &credential)
	if err == nil {
		return credential, nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Message {
		case "authorization_pending":
			return DeviceCredential{}, ErrAuthorizationPending
		case "access_denied":
			return DeviceCredential{}, ErrAccessDenied
		case "expired_token":
			return DeviceCredential{}, ErrDeviceCodeExpired
		}
	}
	return DeviceCredential{}, err
}

// RevokeCurrentCredential revokes the bearer token used by this client.
func (c *Client) RevokeCurrentCredential(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/api/client-auth/session", nil, nil)
}
