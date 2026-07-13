package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewRunID identifies one agent execution so network retries are idempotent
// without treating independent executions as duplicates.
func NewRunID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return "run_" + hex.EncodeToString(raw), nil
}
