package filegate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// UploadsClient contains upload APIs.
type UploadsClient struct {
	core    *clientCore
	Chunked ChunkedUploadClient
}

// ChunkedUploadClient contains resumable chunked upload APIs.
type ChunkedUploadClient struct {
	core *clientCore
}

// ChunkedSendResult models the union response of chunk uploads.
type ChunkedSendResult struct {
	Completed bool
	Progress  *ChunkedProgressResponse
	Complete  *ChunkedCompleteResponse
}

// Start starts or resumes a chunked upload session.
func (c ChunkedUploadClient) Start(ctx context.Context, req ChunkedStartRequest) (*ChunkedStatusResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal chunked start request: %w", err)
	}
	var out ChunkedStatusResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/uploads/chunked/start", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status returns the current chunked upload state.
func (c ChunkedUploadClient) Status(ctx context.Context, uploadID string) (*ChunkedStatusResponse, error) {
	trimmed := strings.TrimSpace(uploadID)
	if trimmed == "" {
		return nil, fmt.Errorf("uploadID is required")
	}
	var out ChunkedStatusResponse
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/uploads/chunked/"+url.PathEscape(trimmed), nil, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendChunk uploads one chunk. The response is either progress or completion.
func (c ChunkedUploadClient) SendChunk(ctx context.Context, uploadID string, index int, data io.Reader, checksum string) (*ChunkedSendResult, error) {
	trimmed := strings.TrimSpace(uploadID)
	if trimmed == "" {
		return nil, fmt.Errorf("uploadID is required")
	}
	if index < 0 {
		return nil, fmt.Errorf("index must be >= 0")
	}

	req, err := c.core.newRequest(
		ctx,
		http.MethodPut,
		"/v1/uploads/chunked/"+url.PathEscape(trimmed)+"/chunks/"+strconv.Itoa(index),
		nil,
		data,
		"application/octet-stream",
	)
	if err != nil {
		return nil, err
	}
	if checksum = strings.TrimSpace(checksum); checksum != "" {
		req.Header.Set("X-Chunk-Checksum", checksum)
	}
	resp, err := c.core.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := ensureSuccess(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read chunk response: %w", err)
	}

	var probe struct {
		Completed bool            `json:"completed"`
		File      json.RawMessage `json:"file"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("decode chunk response probe: %w", err)
	}

	result := &ChunkedSendResult{Completed: probe.Completed}
	if len(probe.File) > 0 {
		var out ChunkedCompleteResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode chunk completion response: %w", err)
		}
		result.Complete = &out
		return result, nil
	}

	var out ChunkedProgressResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode chunk progress response: %w", err)
	}
	result.Progress = &out
	return result, nil
}

// SendChunkRaw uploads one chunk and returns the raw *http.Response
// unchanged — including non-2xx responses. This is the primitive for
// relay/passthrough handlers; use SendChunk when you want the standard
// "throw on non-2xx" behavior.
func (c ChunkedUploadClient) SendChunkRaw(ctx context.Context, uploadID string, index int, data io.Reader, checksum string) (*http.Response, error) {
	trimmed := strings.TrimSpace(uploadID)
	if trimmed == "" {
		return nil, fmt.Errorf("uploadID is required")
	}
	if index < 0 {
		return nil, fmt.Errorf("index must be >= 0")
	}
	req, err := c.core.newRequest(
		ctx,
		http.MethodPut,
		"/v1/uploads/chunked/"+url.PathEscape(trimmed)+"/chunks/"+strconv.Itoa(index),
		nil,
		data,
		"application/octet-stream",
	)
	if err != nil {
		return nil, err
	}
	if checksum = strings.TrimSpace(checksum); checksum != "" {
		req.Header.Set("X-Chunk-Checksum", checksum)
	}
	return c.core.httpClient.Do(req)
}
