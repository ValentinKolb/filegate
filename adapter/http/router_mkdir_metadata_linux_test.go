//go:build linux

package httpadapter

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func authedJSONRequest(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestNodeMetadataIncludesOwnershipAndMimeType(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	baseAbs, err := svc.ResolveAbsPath(root.ID)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	filePath := filepath.Join(baseAbs, "note.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := svc.Rescan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	id, err := svc.ResolvePath(root.Name + "/note.txt")
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/v1/nodes/"+id.String()))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["ownership"]; !ok {
		t.Fatalf("missing ownership object")
	}
	ownership, ok := body["ownership"].(map[string]any)
	if !ok {
		t.Fatalf("ownership is not object: %#v", body["ownership"])
	}
	if ownership["uid"] == nil || ownership["gid"] == nil || ownership["mode"] == nil {
		t.Fatalf("ownership fields missing: %#v", ownership)
	}
	if _, hasLegacy := body["uid"]; hasLegacy {
		t.Fatalf("legacy uid field should not be present")
	}
	if _, hasLegacy := body["gid"]; hasLegacy {
		t.Fatalf("legacy gid field should not be present")
	}
	if _, hasLegacy := body["mode"]; hasLegacy {
		t.Fatalf("legacy mode field should not be present")
	}

	mimeType, ok := body["mimeType"].(string)
	if !ok || strings.TrimSpace(mimeType) == "" {
		t.Fatalf("mimeType missing or empty: %#v", body["mimeType"])
	}
	exifObj, ok := body["exif"].(map[string]any)
	if !ok {
		t.Fatalf("exif missing or not object: %#v", body["exif"])
	}
	if len(exifObj) != 0 {
		t.Fatalf("expected empty exif for text file, got: %#v", exifObj)
	}
}

func TestMkdirRelativeCreatesNestedDirectories(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	reqBody := []byte(`{"path":"a/b/c","recursive":true}`)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/v1/nodes/"+root.ID.String()+"/mkdir", reqBody))
	if w.Result().StatusCode != http.StatusCreated {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["type"] != "directory" {
		t.Fatalf("type=%v, want directory", body["type"])
	}
	path, _ := body["path"].(string)
	if !strings.HasSuffix(path, "/a/b/c") {
		t.Fatalf("path=%q does not end with /a/b/c", path)
	}

	baseAbs, err := svc.ResolveAbsPath(root.ID)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseAbs, "a", "b", "c")); err != nil {
		t.Fatalf("expected directory exists: %v", err)
	}
}

func TestMkdirRejectsUnknownJSONFields(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	reqBody := []byte(`{"path":"a/b","recursive":true,"unknown":123}`)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/v1/nodes/"+root.ID.String()+"/mkdir", reqBody))
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want=%d", w.Result().StatusCode, http.StatusBadRequest)
	}
}

func TestPutDirectoryContentRejected(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/nodes/"+root.ID.String(), bytes.NewReader([]byte("x")))
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want=%d", w.Result().StatusCode, http.StatusBadRequest)
	}
}

func TestStatsEndpointReturnsIndexCacheAndMounts(t *testing.T) {
	r, _, cleanup := newTestRouter(t)
	defer cleanup()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/v1/stats"))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["generatedAt"] == nil {
		t.Fatalf("missing generatedAt")
	}

	indexPart, ok := body["index"].(map[string]any)
	if !ok {
		t.Fatalf("missing index block")
	}
	if indexPart["totalEntities"] == nil || indexPart["totalFiles"] == nil || indexPart["totalDirs"] == nil {
		t.Fatalf("missing index counters: %#v", indexPart)
	}
	if indexPart["dbSizeBytes"] == nil {
		t.Fatalf("missing dbSizeBytes: %#v", indexPart)
	}

	cachePart, ok := body["cache"].(map[string]any)
	if !ok {
		t.Fatalf("missing cache block")
	}
	if cachePart["pathEntries"] == nil || cachePart["pathCapacity"] == nil || cachePart["pathUtilRatio"] == nil {
		t.Fatalf("missing cache counters: %#v", cachePart)
	}

	mounts, ok := body["mounts"].([]any)
	if !ok || len(mounts) == 0 {
		t.Fatalf("missing mounts")
	}

	disks, ok := body["disks"].([]any)
	if !ok || len(disks) == 0 {
		t.Fatalf("missing disks")
	}
	firstDisk, ok := disks[0].(map[string]any)
	if !ok {
		t.Fatalf("disk entry is not object: %#v", disks[0])
	}
	if firstDisk["diskName"] == nil || firstDisk["used"] == nil || firstDisk["size"] == nil {
		t.Fatalf("disk fields missing: %#v", firstDisk)
	}
	roots, ok := firstDisk["roots"].([]any)
	if !ok || len(roots) == 0 {
		t.Fatalf("disk roots missing: %#v", firstDisk["roots"])
	}
}

func TestPathPutOneShotUploadCreatesAndReturnsHeaders(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	target := "/v1/paths/" + root.Name + "/nested/hello.txt"

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader([]byte("hello world")))
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusCreated {
		t.Fatalf("status=%d, want=%d", w.Result().StatusCode, http.StatusCreated)
	}
	nodeID := w.Result().Header.Get("X-Node-Id")
	if strings.TrimSpace(nodeID) == "" {
		t.Fatalf("missing X-Node-Id")
	}
	createdID := w.Result().Header.Get("X-Created-Id")
	if createdID != nodeID {
		t.Fatalf("X-Created-Id=%q, X-Node-Id=%q", createdID, nodeID)
	}

	id, err := svc.ResolvePath(root.Name + "/nested/hello.txt")
	if err != nil {
		t.Fatalf("resolve uploaded path: %v", err)
	}
	if id.String() != nodeID {
		t.Fatalf("resolved id=%q, header id=%q", id.String(), nodeID)
	}
}

func TestPathPutOneShotUploadUpdatesExistingFile(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	target := "/v1/paths/" + root.Name + "/file.txt"

	first := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPut, target, bytes.NewReader([]byte("first")))
	req1.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(first, req1)
	if first.Result().StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d, want=%d", first.Result().StatusCode, http.StatusCreated)
	}
	id := first.Result().Header.Get("X-Node-Id")
	if id == "" {
		t.Fatalf("missing first X-Node-Id")
	}

	second := httptest.NewRecorder()
	// Default is now error — explicit overwrite is required for replace.
	req2 := httptest.NewRequest(http.MethodPut, target+"?onConflict=overwrite", bytes.NewReader([]byte("second")))
	req2.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(second, req2)
	if second.Result().StatusCode != http.StatusOK {
		t.Fatalf("second status=%d, want=%d", second.Result().StatusCode, http.StatusOK)
	}
	if second.Result().Header.Get("X-Created-Id") != "" {
		t.Fatalf("X-Created-Id should be empty on update")
	}
	if got := second.Result().Header.Get("X-Node-Id"); got != id {
		t.Fatalf("updated id=%q, want original id=%q", got, id)
	}

	fileID, err := svc.ResolvePath(root.Name + "/file.txt")
	if err != nil {
		t.Fatalf("resolve file: %v", err)
	}
	rc, _, isDir, err := svc.OpenContent(fileID)
	if err != nil {
		t.Fatalf("open content: %v", err)
	}
	if isDir {
		t.Fatalf("expected file, got directory")
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if string(data) != "second" {
		t.Fatalf("content=%q, want=%q", string(data), "second")
	}
}

func TestIndexResolveReturnsSingleMetadataObjectForPath(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	virtualPath := root.Name + "/resolve-one.txt"
	target := "/v1/paths/" + virtualPath
	put := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader([]byte("x")))
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(put, req)
	if put.Result().StatusCode != http.StatusCreated {
		t.Fatalf("put status=%d", put.Result().StatusCode)
	}

	body := []byte(`{"path":"` + virtualPath + `"}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/v1/index/resolve", body))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	var out struct {
		Item map[string]any `json:"item"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Item == nil {
		t.Fatalf("item is nil")
	}
	id := put.Result().Header.Get("X-Node-Id")
	if id == "" {
		t.Fatalf("missing id header")
	}
	if got, _ := out.Item["id"].(string); got != id {
		t.Fatalf("item.id=%q, want=%q", got, id)
	}
}

func TestIndexResolveReturnsMetadataArrayWithNullForMissingPath(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	virtualPath := root.Name + "/resolve-many.txt"
	target := "/v1/paths/" + virtualPath
	put := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader([]byte("x")))
	req.Header.Set("Authorization", "Bearer test-token")
	r.ServeHTTP(put, req)
	if put.Result().StatusCode != http.StatusCreated {
		t.Fatalf("put status=%d", put.Result().StatusCode)
	}
	id := put.Result().Header.Get("X-Node-Id")
	if id == "" {
		t.Fatalf("missing id header")
	}

	missing := root.Name + "/does-not-exist"
	body := []byte(`{"paths":["` + virtualPath + `","` + missing + `"]}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/v1/index/resolve", body))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	var out struct {
		Items []any `json:"items"`
		Total int   `json:"total"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 2 || len(out.Items) != 2 {
		t.Fatalf("items/total mismatch: len=%d total=%d", len(out.Items), out.Total)
	}

	first, ok := out.Items[0].(map[string]any)
	if !ok {
		t.Fatalf("first item not object: %#v", out.Items[0])
	}
	if got, _ := first["id"].(string); got != id {
		t.Fatalf("first item id=%q, want=%q", got, id)
	}
	if out.Items[1] != nil {
		t.Fatalf("second item should be null, got=%#v", out.Items[1])
	}
}
