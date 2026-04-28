package filegate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// PathsClient contains path-oriented operations.
type PathsClient struct {
	core *clientCore
}

// GetNodeOptions controls optional directory listing behavior on metadata reads.
type GetNodeOptions struct {
	PageSize              int
	Cursor                string
	ComputeRecursiveSizes bool
}

// FileConflictMode mirrors the server's onConflict vocabulary for file-write
// endpoints (PUT /v1/paths, chunked upload start, transfers). The empty
// string is treated as the server default ("error").
type FileConflictMode string

const (
	ConflictError     FileConflictMode = "error"
	ConflictOverwrite FileConflictMode = "overwrite"
	ConflictRename    FileConflictMode = "rename"
)

// MkdirConflictMode mirrors the server's onConflict vocabulary for mkdir.
// The empty string is treated as the server default ("error").
type MkdirConflictMode string

const (
	MkdirConflictError  MkdirConflictMode = "error"
	MkdirConflictSkip   MkdirConflictMode = "skip"
	MkdirConflictRename MkdirConflictMode = "rename"
)

// PutPathOptions controls one-shot path uploads.
type PutPathOptions struct {
	ContentType string
	// OnConflict selects the server-side behavior on a name collision.
	// Empty defaults to the server default ("error").
	OnConflict FileConflictMode
}

// PathPutResponse is returned by one-shot PUT /v1/paths/* uploads.
type PathPutResponse struct {
	Node       Node
	NodeID     string
	CreatedID  string
	StatusCode int
}

func (o GetNodeOptions) toQuery() url.Values {
	query := url.Values{}
	if o.PageSize > 0 {
		query.Set("pageSize", fmt.Sprintf("%d", o.PageSize))
	}
	if o.Cursor != "" {
		query.Set("cursor", o.Cursor)
	}
	if o.ComputeRecursiveSizes {
		query.Set("computeRecursiveSizes", "true")
	}
	return query
}

// List returns all configured roots as nodes.
func (c PathsClient) List(ctx context.Context) (*NodeListResponse, error) {
	var out NodeListResponse
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/paths/", nil, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get fetches metadata for a virtual path. For directories, children can be paged in the same response.
func (c PathsClient) Get(ctx context.Context, virtualPath string, opts GetNodeOptions) (*Node, error) {
	encodedPath, err := encodeVirtualPath(virtualPath)
	if err != nil {
		return nil, err
	}
	var out Node
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/paths/"+encodedPath, opts.toQuery(), nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Put uploads data in one shot to a virtual path. Returns an *APIError on
// non-2xx responses; for passthrough use PutRaw.
func (c PathsClient) Put(ctx context.Context, virtualPath string, data io.Reader, opts PutPathOptions) (*PathPutResponse, error) {
	resp, err := c.PutRaw(ctx, virtualPath, data, opts)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := ensureSuccess(resp); err != nil {
		return nil, err
	}

	var node Node
	if err := json.NewDecoder(resp.Body).Decode(&node); err != nil {
		return nil, fmt.Errorf("decode put path response: %w", err)
	}
	return &PathPutResponse{
		Node:       node,
		NodeID:     resp.Header.Get("X-Node-Id"),
		CreatedID:  resp.Header.Get("X-Created-Id"),
		StatusCode: resp.StatusCode,
	}, nil
}

// PutRaw uploads data and returns the raw *http.Response unchanged — including
// non-2xx responses. This is the primitive for relay/passthrough handlers
// that need to forward the upstream status, headers, and body to a
// downstream client without rewriting them. Use Put when you want the
// standard "throw on non-2xx" behavior.
func (c PathsClient) PutRaw(ctx context.Context, virtualPath string, data io.Reader, opts PutPathOptions) (*http.Response, error) {
	encodedPath, err := encodeVirtualPath(virtualPath)
	if err != nil {
		return nil, err
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	query := url.Values{}
	if opts.OnConflict != "" {
		query.Set("onConflict", string(opts.OnConflict))
	}
	return c.core.doRaw(ctx, http.MethodPut, "/v1/paths/"+encodedPath, query, data, contentType)
}
