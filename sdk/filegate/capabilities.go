package filegate

import (
	"context"
	"net/http"
)

// CapabilitiesClient contains server capability endpoints.
type CapabilitiesClient struct {
	core *clientCore
}

func (c CapabilitiesClient) Get(ctx context.Context) (*CapabilitiesResponse, error) {
	var out CapabilitiesResponse
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/capabilities", nil, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
