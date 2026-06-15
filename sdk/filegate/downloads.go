package filegate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// DownloadsClient contains direct download URL APIs.
type DownloadsClient struct {
	core *clientCore
}

// CreateDirectURL mints a scoped signed GET URL for one file or directory.
func (c DownloadsClient) CreateDirectURL(ctx context.Context, req DirectDownloadURLRequest) (*DirectDownloadURLResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal direct download url request: %w", err)
	}
	var out DirectDownloadURLResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/downloads/direct", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
