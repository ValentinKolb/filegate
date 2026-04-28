package filegate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// IndexClient contains index maintenance and lookup operations.
type IndexClient struct {
	core *clientCore
}

func (c IndexClient) Rescan(ctx context.Context) (*OKResponse, error) {
	var out OKResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/index/rescan", nil, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c IndexClient) ResolvePath(ctx context.Context, path string) (*Node, error) {
	req := IndexResolveRequest{Path: path}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal resolve request: %w", err)
	}
	var out IndexResolveSingleResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/index/resolve", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}

func (c IndexClient) ResolvePaths(ctx context.Context, paths []string) (*IndexResolveManyResponse, error) {
	req := IndexResolveRequest{Paths: paths}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal resolve request: %w", err)
	}
	var out IndexResolveManyResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/index/resolve", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c IndexClient) ResolveID(ctx context.Context, id string) (*Node, error) {
	req := IndexResolveRequest{ID: id}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal resolve request: %w", err)
	}
	var out IndexResolveSingleResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/index/resolve", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}

func (c IndexClient) ResolveIDs(ctx context.Context, ids []string) (*IndexResolveManyResponse, error) {
	req := IndexResolveRequest{IDs: ids}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal resolve request: %w", err)
	}
	var out IndexResolveManyResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/index/resolve", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
