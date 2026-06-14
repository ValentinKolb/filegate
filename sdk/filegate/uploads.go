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
	core     *clientCore
	Sessions UploadSessionsClient
}

// UploadSessionsClient contains resumable upload-session APIs.
type UploadSessionsClient struct {
	core *clientCore
}

type UploadSessionStatusRequest struct {
	SessionID string
}

type UploadSessionPutSegmentRequest struct {
	SessionID   string
	Index       int
	Body        io.Reader
	Checksum    string
	ContentType string
}

type UploadSessionCommitRequest struct {
	SessionID string
}

type UploadSessionAbortRequest struct {
	SessionID string
}

// CreateDirectUploadURL mints a scoped signed PUT URL for one whole-file upload.
func (c UploadsClient) CreateDirectUploadURL(ctx context.Context, req DirectUploadURLRequest) (*DirectUploadURLResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal direct upload url request: %w", err)
	}
	var out DirectUploadURLResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/uploads/direct", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Create creates one resumable upload session for one file.
func (c UploadSessionsClient) Create(ctx context.Context, req UploadSessionCreateRequest) (*UploadSessionResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal upload session request: %w", err)
	}
	var out UploadSessionResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/uploads/sessions", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateBatch creates independent upload sessions in one request.
func (c UploadSessionsClient) CreateBatch(ctx context.Context, req UploadSessionBatchCreateRequest) (*UploadSessionBatchCreateResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal upload session batch request: %w", err)
	}
	var out UploadSessionBatchCreateResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/uploads/sessions:batch", nil, bytes.NewReader(payload), "application/json", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status returns current session state.
func (c UploadSessionsClient) Status(ctx context.Context, req UploadSessionStatusRequest) (*UploadSessionResponse, error) {
	sessionID, err := requireUploadSessionID(req.SessionID)
	if err != nil {
		return nil, err
	}
	var out UploadSessionResponse
	if err := c.core.doJSON(ctx, http.MethodGet, "/v1/uploads/sessions/"+url.PathEscape(sessionID), nil, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutSegment uploads one segment and throws on non-2xx responses.
func (c UploadSessionsClient) PutSegment(ctx context.Context, req UploadSessionPutSegmentRequest) (*UploadSegmentResponse, error) {
	resp, err := c.PutSegmentRaw(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := ensureSuccess(resp); err != nil {
		return nil, err
	}
	var out UploadSegmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode upload segment response: %w", err)
	}
	return &out, nil
}

// PutSegmentRaw uploads one segment and returns the raw response for relays.
func (c UploadSessionsClient) PutSegmentRaw(ctx context.Context, req UploadSessionPutSegmentRequest) (*http.Response, error) {
	sessionID, err := requireUploadSessionID(req.SessionID)
	if err != nil {
		return nil, err
	}
	if req.Index < 0 {
		return nil, fmt.Errorf("index must be >= 0")
	}
	contentType := strings.TrimSpace(req.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	httpReq, err := c.core.newRequest(
		ctx,
		http.MethodPut,
		"/v1/uploads/sessions/"+url.PathEscape(sessionID)+"/segments/"+strconv.Itoa(req.Index),
		nil,
		req.Body,
		contentType,
	)
	if err != nil {
		return nil, err
	}
	if checksum := strings.TrimSpace(req.Checksum); checksum != "" {
		httpReq.Header.Set("X-Segment-Checksum", checksum)
	}
	return c.core.httpClient.Do(httpReq)
}

// Commit finalizes a complete session.
func (c UploadSessionsClient) Commit(ctx context.Context, req UploadSessionCommitRequest) (*UploadSessionCommitResponse, error) {
	sessionID, err := requireUploadSessionID(req.SessionID)
	if err != nil {
		return nil, err
	}
	var out UploadSessionCommitResponse
	if err := c.core.doJSON(ctx, http.MethodPost, "/v1/uploads/sessions/"+url.PathEscape(sessionID)+"/commit", nil, nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Abort aborts an in-progress session.
func (c UploadSessionsClient) Abort(ctx context.Context, req UploadSessionAbortRequest) error {
	sessionID, err := requireUploadSessionID(req.SessionID)
	if err != nil {
		return err
	}
	resp, err := c.core.doRaw(ctx, http.MethodDelete, "/v1/uploads/sessions/"+url.PathEscape(sessionID), nil, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return ensureSuccess(resp)
}

func requireUploadSessionID(sessionID string) (string, error) {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return "", fmt.Errorf("sessionID is required")
	}
	return trimmed, nil
}
