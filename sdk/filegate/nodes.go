package filegate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NodesClient contains ID-oriented node operations.
type NodesClient struct {
	core *clientCore
}

// ThumbnailOptions configures thumbnail generation.
type ThumbnailOptions struct {
	Size int
}

// PipeResult contains relay-relevant metadata after a streamed transfer.
type PipeResult struct {
	StatusCode int
	Header     http.Header
	Bytes      int64
}

// Get fetches metadata for a node ID. For directories, children can be paged in the same response.
func (c NodesClient) Get(ctx context.Context, id string, opts GetNodeOptions) (*Node, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, fmt.Errorf("id is required")
	}
	var out Node
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(trimmed), opts.toQuery(), nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ContentRaw streams file content or a tar stream (for directories) and
// returns the raw *http.Response unchanged — including non-2xx responses.
// This is the primitive for relay/passthrough handlers; use PipeContent
// when you want the standard "throw on non-2xx" behavior.
func (c NodesClient) ContentRaw(ctx context.Context, id string, inline bool) (*http.Response, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, fmt.Errorf("id is required")
	}
	query := url.Values{}
	boolQuery(query, "inline", inline)
	return c.core.doRaw(ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(trimmed)+"/content", query, nil, "")
}

// PipeContent streams node content directly into dst. Returns *APIError on
// non-2xx responses (no bytes are written to dst in that case).
func (c NodesClient) PipeContent(ctx context.Context, id string, inline bool, dst io.Writer) (*PipeResult, error) {
	resp, err := c.ContentRaw(ctx, id, inline)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := ensureSuccess(resp); err != nil {
		return nil, err
	}

	n, err := io.Copy(dst, resp.Body)
	if err != nil {
		return nil, err
	}
	return &PipeResult{
		StatusCode: resp.StatusCode,
		Header:     cloneHeader(resp.Header),
		Bytes:      n,
	}, nil
}

// PutContent replaces content of a file node.
func (c NodesClient) PutContent(ctx context.Context, id string, data io.Reader, contentType string) (*Node, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, fmt.Errorf("id is required")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var out Node
	if err := c.core.doJSON(ctx, http.MethodPut, "/v1/nodes/"+url.PathEscape(trimmed), nil, data, contentType, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Mkdir creates a child directory under a parent node ID.
func (c NodesClient) Mkdir(ctx context.Context, parentID string, req MkdirRequest) (*Node, error) {
	trimmed := strings.TrimSpace(parentID)
	if trimmed == "" {
		return nil, fmt.Errorf("parent id is required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal mkdir request: %w", err)
	}
	var out Node
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(trimmed)+"/mkdir", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Patch updates node metadata (rename and/or ownership).
func (c NodesClient) Patch(ctx context.Context, id string, req UpdateNodeRequest, recursiveOwnership *bool) (*Node, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, fmt.Errorf("id is required")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal update request: %w", err)
	}
	query := url.Values{}
	if recursiveOwnership != nil {
		query.Set("recursiveOwnership", fmt.Sprintf("%t", *recursiveOwnership))
	}
	var out Node
	if err := c.core.doJSON(ctx, http.MethodPatch, "/v1/nodes/"+url.PathEscape(trimmed), query, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a node subtree.
func (c NodesClient) Delete(ctx context.Context, id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("id is required")
	}
	resp, err := c.core.doRaw(ctx, http.MethodDelete, "/v1/nodes/"+url.PathEscape(trimmed), nil, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := ensureSuccess(resp); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// ThumbnailRaw returns the raw thumbnail response unchanged (including
// non-2xx). Use this for relay/passthrough handlers.
func (c NodesClient) ThumbnailRaw(ctx context.Context, id string, opts ThumbnailOptions) (*http.Response, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, fmt.Errorf("id is required")
	}
	query := url.Values{}
	if opts.Size > 0 {
		query.Set("size", fmt.Sprintf("%d", opts.Size))
	}
	return c.core.doRaw(ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(trimmed)+"/thumbnail", query, nil, "")
}
