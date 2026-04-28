package filegate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatalf("expected error for missing base URL")
	}
}

func TestPathsPutReturnsHeadersAndNode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method=%s", r.Method)
		}
		if got, want := r.URL.EscapedPath(), "/v1/paths/mount/nested/hello%20world.txt"; got != want {
			t.Fatalf("path=%q want=%q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer secret"; got != want {
			t.Fatalf("auth=%q want=%q", got, want)
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("content-type=%q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "hello" {
			t.Fatalf("body=%q", string(body))
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Node-Id", "11111111-1111-1111-1111-111111111111")
		w.Header().Set("X-Created-Id", "11111111-1111-1111-1111-111111111111")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Node{
			ID:    "11111111-1111-1111-1111-111111111111",
			Type:  "file",
			Name:  "hello world.txt",
			Path:  "/mount/nested/hello world.txt",
			Size:  5,
			Mtime: 1,
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "secret"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	res, err := client.Paths.Put(context.Background(), "mount/nested/hello world.txt", strings.NewReader("hello"), PutPathOptions{})
	if err != nil {
		t.Fatalf("put path: %v", err)
	}
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if res.NodeID == "" || res.CreatedID == "" || res.NodeID != res.CreatedID {
		t.Fatalf("unexpected ids: node=%q created=%q", res.NodeID, res.CreatedID)
	}
	if res.Node.Path != "/mount/nested/hello world.txt" {
		t.Fatalf("node.path=%q", res.Node.Path)
	}
}

func TestNodesPipeContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/abc/content" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var dst bytes.Buffer
	out, err := client.Nodes.PipeContent(context.Background(), "abc", false, &dst)
	if err != nil {
		t.Fatalf("pipe content: %v", err)
	}
	if out.Bytes != 7 {
		t.Fatalf("bytes=%d", out.Bytes)
	}
	if dst.String() != "payload" {
		t.Fatalf("payload=%q", dst.String())
	}
	if out.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("missing content-type in pipe result")
	}
}

func TestChunkedSendChunkParsesUnion(t *testing.T) {
	t.Parallel()

	t.Run("progress", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-Chunk-Checksum"); got != "sha256:abc" {
				t.Fatalf("checksum header=%q", got)
			}
			_ = json.NewEncoder(w).Encode(ChunkedProgressResponse{
				ChunkIndex:     0,
				UploadedChunks: []int{0},
				Completed:      false,
			})
		}))
		defer server.Close()

		client, err := New(Config{BaseURL: server.URL})
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		res, err := client.Uploads.Chunked.SendChunk(context.Background(), "upload-1", 0, strings.NewReader("x"), "sha256:abc")
		if err != nil {
			t.Fatalf("send chunk: %v", err)
		}
		if res.Completed {
			t.Fatalf("expected not completed")
		}
		if res.Progress == nil || res.Progress.ChunkIndex != 0 {
			t.Fatalf("progress missing: %#v", res.Progress)
		}
		if res.Complete != nil {
			t.Fatalf("complete should be nil")
		}
	})

	t.Run("complete", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ChunkedCompleteResponse{
				Completed: true,
				File: NodeWithChecksum{
					Node:     Node{ID: "n1", Type: "file", Name: "x", Path: "/x"},
					Checksum: "sha256:done",
				},
			})
		}))
		defer server.Close()

		client, err := New(Config{BaseURL: server.URL})
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		res, err := client.Uploads.Chunked.SendChunk(context.Background(), "upload-1", 1, strings.NewReader("x"), "")
		if err != nil {
			t.Fatalf("send chunk: %v", err)
		}
		if !res.Completed {
			t.Fatalf("expected completed=true")
		}
		if res.Complete == nil || res.Complete.File.Checksum != "sha256:done" {
			t.Fatalf("completion missing: %#v", res.Complete)
		}
		if res.Progress != nil {
			t.Fatalf("progress should be nil")
		}
	})
}

func TestAPIErrorIncludesStatusAndMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "not found"})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.Paths.Get(context.Background(), "missing/path", GetNodeOptions{})
	if err == nil {
		t.Fatalf("expected api error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", apiErr.StatusCode)
	}
	if apiErr.Message != "not found" {
		t.Fatalf("message=%q", apiErr.Message)
	}
}

func TestIndexResolvePathsSupportsNullItems(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"x","type":"file","name":"a","path":"/a","size":1,"mtime":1,"ownership":{"uid":1,"gid":1,"mode":"644"}},null],"total":2}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	out, err := client.Index.ResolvePaths(context.Background(), []string{"a", "missing"})
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}
	if out.Total != 2 || len(out.Items) != 2 {
		t.Fatalf("unexpected total/items: %d/%d", out.Total, len(out.Items))
	}
	if out.Items[0] == nil || out.Items[0].ID != "x" {
		t.Fatalf("unexpected first item: %#v", out.Items[0])
	}
	if out.Items[1] != nil {
		t.Fatalf("second item should be nil")
	}
}

// --- regression tests for SDK <-> server contract ---

// TestPathsPutOnConflictIsForwardedAsQuery pins that PutPathOptions.OnConflict
// reaches the server as a query parameter — the field was missing from the SDK
// before the conflict-handling refactor was finished.
func TestPathsPutOnConflictIsForwardedAsQuery(t *testing.T) {
	t.Parallel()

	var seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query().Get("onConflict")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Node-Id", "id")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Node{ID: "id", Type: "file", Name: "x", Path: "/m/x"})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	for _, mode := range []FileConflictMode{ConflictError, ConflictOverwrite, ConflictRename} {
		seenQuery = ""
		_, err := client.Paths.Put(context.Background(), "m/x", strings.NewReader("body"), PutPathOptions{OnConflict: mode})
		if err != nil {
			t.Fatalf("mode=%q: %v", mode, err)
		}
		if seenQuery != string(mode) {
			t.Fatalf("mode=%q: server saw onConflict=%q", mode, seenQuery)
		}
	}

	// Empty mode => no query param sent (server uses its default).
	seenQuery = ""
	if _, err := client.Paths.Put(context.Background(), "m/x", strings.NewReader("body"), PutPathOptions{}); err != nil {
		t.Fatalf("default mode: %v", err)
	}
	if seenQuery != "" {
		t.Fatalf("default mode should send no onConflict query, got %q", seenQuery)
	}
}

// TestRawMethodsDoNotThrowOnNon2xx pins the contract that *Raw methods return
// the response unchanged on 4xx/5xx so relay handlers can forward it. This was
// the relay-breaking bug that caused 409 conflict bodies to be swallowed.
func TestRawMethodsDoNotThrowOnNon2xx(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorResponse{
			Error:        "filename already exists in parent",
			ExistingID:   "01933abc-1234-7000-8000-000000000001",
			ExistingPath: "mount/foo.txt",
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name string
		fn   func() (*http.Response, error)
	}{
		{"PutRaw", func() (*http.Response, error) {
			return client.Paths.PutRaw(ctx, "m/x", strings.NewReader("body"), PutPathOptions{})
		}},
		{"ContentRaw", func() (*http.Response, error) {
			return client.Nodes.ContentRaw(ctx, "any-id", false)
		}},
		{"ThumbnailRaw", func() (*http.Response, error) {
			return client.Nodes.ThumbnailRaw(ctx, "any-id", ThumbnailOptions{Size: 256})
		}},
		{"SendChunkRaw", func() (*http.Response, error) {
			return client.Uploads.Chunked.SendChunkRaw(ctx, "uploadid", 0, strings.NewReader("data"), "")
		}},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			resp, err := c.fn()
			if err != nil {
				t.Fatalf("expected no error from %s on 409, got %v", c.name, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusConflict)
			}
			body, _ := io.ReadAll(resp.Body)
			if !bytes.Contains(body, []byte("existingId")) {
				t.Fatalf("expected diagnostic body, got %q", body)
			}
		})
	}
}

// TestAPIErrorPopulatesConflictDiagnostics pins that 409 responses with
// existingId/existingPath surface as typed fields on APIError, not buried in
// the raw Body string.
func TestAPIErrorPopulatesConflictDiagnostics(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorResponse{
			Error:        "filename already exists in parent",
			ExistingID:   "01933abc-1234-7000-8000-000000000001",
			ExistingPath: "mount/foo.txt",
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.Paths.Put(context.Background(), "m/foo.txt", strings.NewReader("body"), PutPathOptions{OnConflict: ConflictError})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T (%v)", err, err)
	}
	if !apiErr.IsConflict() {
		t.Fatalf("IsConflict()=false, want true")
	}
	if apiErr.ExistingID != "01933abc-1234-7000-8000-000000000001" {
		t.Fatalf("ExistingID=%q", apiErr.ExistingID)
	}
	if apiErr.ExistingPath != "mount/foo.txt" {
		t.Fatalf("ExistingPath=%q", apiErr.ExistingPath)
	}
	if !strings.Contains(apiErr.Body, "existingId") {
		t.Fatalf("raw Body should still contain payload, got %q", apiErr.Body)
	}
}
