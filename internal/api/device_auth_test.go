package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceAuthorizationMapsPendingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("public request included an Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer server.Close()

	client, err := NewPublic(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ExchangeDeviceAuthorization(context.Background(), "device-code")
	if !errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("err = %v, want ErrAuthorizationPending", err)
	}
}
