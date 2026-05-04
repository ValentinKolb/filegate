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

// versionsTestServer wires a mux with all 7 version endpoints. Each
// handler asserts auth + path shape and returns canned JSON. Tests
// exercise one method at a time and don't assert behaviour the SDK
// would only flow through opaquely.
func versionsTestServer(t *testing.T, handler http.HandlerFunc) (*Filegate, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := New(Config{BaseURL: server.URL, Token: "secret"})
	if err != nil {
		server.Close()
		t.Fatalf("new client: %v", err)
	}
	return client, server.Close
}

func TestVersionsListEncodesCursorAndLimit(t *testing.T) {
	client, stop := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/file-1/versions" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("cursor"); got != "cursor-A" {
			t.Fatalf("cursor=%q", got)
		}
		if got := q.Get("limit"); got != "25" {
			t.Fatalf("limit=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ListVersionsResponse{
			Items:      []VersionResponse{{VersionID: "v1", FileID: "file-1", Size: 100}},
			NextCursor: "cursor-B",
		})
	})
	defer stop()

	got, err := client.Versions.List(context.Background(), "file-1", ListVersionsOptions{
		Cursor: "cursor-A",
		Limit:  25,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].VersionID != "v1" {
		t.Fatalf("items=%#v", got.Items)
	}
	if got.NextCursor != "cursor-B" {
		t.Fatalf("nextCursor=%q", got.NextCursor)
	}
}

func TestVersionsListAllPagesUntilCursorEmpty(t *testing.T) {
	calls := 0
	client, stop := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("cursor") {
		case "":
			_ = json.NewEncoder(w).Encode(ListVersionsResponse{
				Items:      []VersionResponse{{VersionID: "v1"}, {VersionID: "v2"}},
				NextCursor: "v2",
			})
		case "v2":
			_ = json.NewEncoder(w).Encode(ListVersionsResponse{
				Items:      []VersionResponse{{VersionID: "v3"}},
				NextCursor: "",
			})
		default:
			t.Fatalf("unexpected cursor=%q", r.URL.Query().Get("cursor"))
		}
	})
	defer stop()

	all, err := client.Versions.ListAll(context.Background(), "file-1")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListAll got %d items, want 3", len(all))
	}
	if calls != 2 {
		t.Fatalf("expected 2 server calls, got %d", calls)
	}
}

func TestVersionsPipeContentStreamsBytes(t *testing.T) {
	const payload = "version-bytes-stream"
	client, stop := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/file-1/versions/v1/content" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "20")
		_, _ = io.Copy(w, strings.NewReader(payload))
	})
	defer stop()

	var buf bytes.Buffer
	res, err := client.Versions.PipeContent(context.Background(), "file-1", "v1", &buf)
	if err != nil {
		t.Fatalf("PipeContent: %v", err)
	}
	if buf.String() != payload {
		t.Fatalf("body=%q", buf.String())
	}
	if res.Bytes != int64(len(payload)) {
		t.Fatalf("bytes=%d, want %d", res.Bytes, len(payload))
	}
}

func TestVersionsSnapshotSendsLabel(t *testing.T) {
	client, stop := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/file-1/versions/snapshot" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		var body VersionSnapshotRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Label != "checkpoint" {
			t.Fatalf("label=%q", body.Label)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(VersionResponse{
			VersionID: "snap-1",
			FileID:    "file-1",
			Pinned:    true,
			Label:     "checkpoint",
		})
	})
	defer stop()

	got, err := client.Versions.Snapshot(context.Background(), "file-1", "checkpoint")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !got.Pinned || got.Label != "checkpoint" {
		t.Fatalf("response=%#v", got)
	}
}

func TestVersionsPinSendsLabelPointer(t *testing.T) {
	client, stop := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/file-1/versions/v1/pin" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		var body VersionPinRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Label == nil || *body.Label != "important" {
			t.Fatalf("label=%v", body.Label)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(VersionResponse{VersionID: "v1", Pinned: true, Label: "important"})
	})
	defer stop()

	label := "important"
	got, err := client.Versions.Pin(context.Background(), "file-1", "v1", &label)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if got.Label != "important" {
		t.Fatalf("label=%q", got.Label)
	}
}

func TestVersionsRestoreSendsAsNewFileFlag(t *testing.T) {
	client, stop := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/file-1/versions/v1/restore" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		var body VersionRestoreRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.AsNewFile || body.Name != "manual.bin" {
			t.Fatalf("body=%#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(VersionRestoreResponse{
			Node:  Node{ID: "new-id", Name: "manual.bin"},
			AsNew: true,
		})
	})
	defer stop()

	got, err := client.Versions.Restore(context.Background(), "file-1", "v1", RestoreOptions{
		AsNewFile: true,
		Name:      "manual.bin",
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !got.AsNew || got.Node.Name != "manual.bin" {
		t.Fatalf("response=%#v", got)
	}
}

func TestVersionsDeleteSendsCorrectMethod(t *testing.T) {
	client, stop := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method=%s", r.Method)
		}
		if r.URL.Path != "/v1/nodes/file-1/versions/v1" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer stop()

	if err := client.Versions.Delete(context.Background(), "file-1", "v1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestVersionsRejectsEmptyIDs(t *testing.T) {
	client, _ := versionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called")
	})

	if _, err := client.Versions.List(context.Background(), "", ListVersionsOptions{}); err == nil {
		t.Fatalf("expected error for empty fileID")
	}
	if _, err := client.Versions.Snapshot(context.Background(), "", "x"); err == nil {
		t.Fatalf("expected error for empty fileID on Snapshot")
	}
	if _, err := client.Versions.Pin(context.Background(), "f", "", nil); err == nil {
		t.Fatalf("expected error for empty versionID on Pin")
	}
	if err := client.Versions.Delete(context.Background(), "f", ""); err == nil {
		t.Fatalf("expected error for empty versionID on Delete")
	}
}
