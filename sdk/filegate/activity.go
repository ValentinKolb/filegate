package filegate

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type ActivityClient struct {
	core *clientCore
}

type ActivityListOptions struct {
	Limit     int
	Offset    int
	Query     string
	Operation string
	Outcome   string
}

func (c ActivityClient) List(ctx context.Context, opts ActivityListOptions) (*ActivityListResponse, error) {
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	if opts.Query != "" {
		q.Set("q", opts.Query)
	}
	if opts.Operation != "" {
		q.Set("operation", opts.Operation)
	}
	if opts.Outcome != "" {
		q.Set("outcome", opts.Outcome)
	}
	var out ActivityListResponse
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/activity", q, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
