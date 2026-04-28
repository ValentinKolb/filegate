//go:build linux

package httpadapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNodeChildrenAreDirsFirstWithStableCursor(t *testing.T) {
	r, svc, cleanup := newTestRouter(t)
	defer cleanup()

	root := svc.ListRoot()[0]
	if _, err := svc.CreateChild(root.ID, "zdir", true, nil); err != nil {
		t.Fatalf("create zdir: %v", err)
	}
	if _, err := svc.CreateChild(root.ID, "adir", true, nil); err != nil {
		t.Fatalf("create adir: %v", err)
	}
	if _, err := svc.CreateChild(root.ID, "a.txt", false, nil); err != nil {
		t.Fatalf("create a.txt: %v", err)
	}
	if _, err := svc.CreateChild(root.ID, "0.txt", false, nil); err != nil {
		t.Fatalf("create 0.txt: %v", err)
	}

	req1 := authedRequest(http.MethodGet, "/v1/nodes/"+root.ID.String()+"?pageSize=2")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Result().StatusCode != http.StatusOK {
		t.Fatalf("page1 status=%d", w1.Result().StatusCode)
	}
	var page1 struct {
		Children []struct {
			Name string `json:"name"`
		} `json:"children"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.NewDecoder(w1.Result().Body).Decode(&page1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(page1.Children) != 2 {
		t.Fatalf("page1 child count=%d, want=2", len(page1.Children))
	}
	if page1.Children[0].Name != "adir" || page1.Children[1].Name != "zdir" {
		t.Fatalf("page1 order=%q,%q want adir,zdir", page1.Children[0].Name, page1.Children[1].Name)
	}
	if page1.NextCursor != "zdir" {
		t.Fatalf("page1 nextCursor=%q, want zdir", page1.NextCursor)
	}

	req2 := authedRequest(http.MethodGet, "/v1/nodes/"+root.ID.String()+"?pageSize=10&cursor="+page1.NextCursor)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Result().StatusCode != http.StatusOK {
		t.Fatalf("page2 status=%d", w2.Result().StatusCode)
	}
	var page2 struct {
		Children []struct {
			Name string `json:"name"`
		} `json:"children"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.NewDecoder(w2.Result().Body).Decode(&page2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(page2.Children) != 2 {
		t.Fatalf("page2 child count=%d, want=2", len(page2.Children))
	}
	if page2.Children[0].Name != "0.txt" || page2.Children[1].Name != "a.txt" {
		t.Fatalf("page2 order=%q,%q want 0.txt,a.txt", page2.Children[0].Name, page2.Children[1].Name)
	}
	if page2.NextCursor != "" {
		t.Fatalf("page2 nextCursor=%q, want empty", page2.NextCursor)
	}
}
