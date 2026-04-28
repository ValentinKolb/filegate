package filegate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// TransfersClient contains move/copy operations.
type TransfersClient struct {
	core *clientCore
}

// Create executes a transfer operation.
func (c TransfersClient) Create(ctx context.Context, req TransferRequest, recursiveOwnership *bool) (*TransferResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal transfer request: %w", err)
	}
	query := url.Values{}
	if recursiveOwnership != nil {
		query.Set("recursiveOwnership", fmt.Sprintf("%t", *recursiveOwnership))
	}
	var out TransferResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/transfers", query, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
