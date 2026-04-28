package filegate

import (
	"context"
	"net/http"
)

// StatsClient contains statistics endpoints.
type StatsClient struct {
	core *clientCore
}

func (c StatsClient) Get(ctx context.Context) (*StatsResponse, error) {
	var out StatsResponse
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/stats", nil, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
