package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
)

// sessionConfig reads FLUXOR_SESSION_SECRET and FLUXOR_COOKIE_SECURE. Errors
// never quote the secret.
func sessionConfig() (api.SessionConfig, error) {
	cfg := api.SessionConfig{Secure: os.Getenv("FLUXOR_COOKIE_SECURE") == "true"}
	raw := os.Getenv("FLUXOR_SESSION_SECRET")
	if raw == "" {
		log.Warn().Msg("FLUXOR_SESSION_SECRET is unset, so a random one is used and browser sessions end on every restart")
		cfg.Secret = api.RandomSessionSecret()
		return cfg, nil
	}
	secret, err := parseSessionSecret(raw)
	if err != nil {
		return cfg, fmt.Errorf("FLUXOR_SESSION_SECRET: %w", err)
	}
	cfg.Secret = secret
	return cfg, nil
}

func parseSessionSecret(raw string) ([]byte, error) {
	secret, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("not valid base64")
	}
	if len(secret) < api.MinSessionSecret {
		return nil, fmt.Errorf("decodes to %d bytes, want at least %d", len(secret), api.MinSessionSecret)
	}
	return secret, nil
}
