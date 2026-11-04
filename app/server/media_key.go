package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"doexam/pkg/mediaurl"
	"doexam/pkg/storage"
)

const mediaKeyFile = "system/media-signing.key"

// mediaSigningKey returns the configured key, or the one kept in storage (created on first
// start), so a single-binary install needs no extra setting and links survive restarts.
func mediaSigningKey(ctx context.Context, configured string, files *storage.LocalStore) ([]byte, error) {
	if configured != "" {
		k, err := mediaurl.DecodeKey(configured)
		if err != nil {
			return nil, fmt.Errorf("MEDIA_SIGNING_KEY: %w", err)
		}
		return k, nil
	}
	if b, err := files.Get(ctx, mediaKeyFile); err == nil {
		if k, err := mediaurl.DecodeKey(strings.TrimSpace(string(b))); err == nil {
			return k, nil
		}
		return nil, fmt.Errorf("%s in storage is not a valid key", mediaKeyFile)
	}
	k, err := mediaurl.NewKey()
	if err != nil {
		return nil, err
	}
	if err := files.Put(ctx, mediaKeyFile, []byte(hex.EncodeToString(k))); err != nil {
		return nil, fmt.Errorf("store media signing key: %w", err)
	}
	return k, nil
}
