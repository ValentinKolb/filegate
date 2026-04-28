//go:build linux

package domain_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/eventbus"
	"github.com/valentinkolb/filegate/infra/filesystem"
	indexpebble "github.com/valentinkolb/filegate/infra/pebble"
)

func newServiceForOwnershipTest(t *testing.T) (*domain.Service, func()) {
	t.Helper()

	baseDir := t.TempDir()
	indexDir := t.TempDir()
	idx, err := indexpebble.Open(indexDir, 16<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	store := filesystem.New()
	bus := eventbus.New()
	svc, err := domain.NewService(idx, store, bus, []string{baseDir}, 1000)
	if err != nil {
		_ = idx.Close()
		t.Fatalf("new service: %v", err)
	}
	cleanup := func() { _ = idx.Close() }
	return svc, cleanup
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.Mode().Perm()
}

func TestTransferAppliesModeAndDirModeRecursively(t *testing.T) {
	svc, cleanup := newServiceForOwnershipTest(t)
	defer cleanup()

	root := svc.ListRoot()[0].ID

	src, err := svc.CreateChild(root, "src", true, nil)
	if err != nil {
		t.Fatalf("create src dir: %v", err)
	}
	sub, err := svc.CreateChild(src.ID, "sub", true, nil)
	if err != nil {
		t.Fatalf("create sub dir: %v", err)
	}
	if _, err := svc.CreateChild(src.ID, "a.txt", false, nil); err != nil {
		t.Fatalf("create file a.txt: %v", err)
	}
	if _, err := svc.CreateChild(sub.ID, "b.txt", false, nil); err != nil {
		t.Fatalf("create file b.txt: %v", err)
	}

	out, err := svc.Transfer(domain.TransferRequest{
		Op:             "copy",
		SourceID:       src.ID,
		TargetParentID: root,
		TargetName:     "dst",
		OnConflict:     "error",
		Ownership: &domain.Ownership{
			Mode:    "600",
			DirMode: "700",
		},
	})
	if err != nil {
		t.Fatalf("transfer copy: %v", err)
	}

	dstPath, err := svc.ResolveAbsPath(out.ID)
	if err != nil {
		t.Fatalf("resolve dst path: %v", err)
	}

	if got := modeOf(t, dstPath); got != 0o700 {
		t.Fatalf("dst dir mode = %o, want %o", got, 0o700)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub")); got != 0o700 {
		t.Fatalf("dst sub dir mode = %o, want %o", got, 0o700)
	}
	if got := modeOf(t, filepath.Join(dstPath, "a.txt")); got != 0o600 {
		t.Fatalf("dst a.txt mode = %o, want %o", got, 0o600)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub", "b.txt")); got != 0o600 {
		t.Fatalf("dst sub/b.txt mode = %o, want %o", got, 0o600)
	}
}

func TestOwnershipValidationRejectsOnlyUID(t *testing.T) {
	svc, cleanup := newServiceForOwnershipTest(t)
	defer cleanup()

	root := svc.ListRoot()[0].ID
	_, err := svc.CreateChild(root, "x", false, &domain.Ownership{UID: ptrInt(1000)})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument, got %v", err)
	}
}

func TestUpdateNodePatchRenamesAndAppliesRecursiveModes(t *testing.T) {
	svc, cleanup := newServiceForOwnershipTest(t)
	defer cleanup()

	root := svc.ListRoot()[0].ID
	dir, err := svc.CreateChild(root, "patch-src", true, nil)
	if err != nil {
		t.Fatalf("create patch-src dir: %v", err)
	}
	sub, err := svc.CreateChild(dir.ID, "sub", true, nil)
	if err != nil {
		t.Fatalf("create sub dir: %v", err)
	}
	if _, err := svc.CreateChild(sub.ID, "x.txt", false, nil); err != nil {
		t.Fatalf("create file x.txt: %v", err)
	}

	newName := "patch-dst"
	updated, err := svc.UpdateNode(dir.ID, &newName, &domain.Ownership{
		Mode:    "640",
		DirMode: "750",
	}, true)
	if err != nil {
		t.Fatalf("patch update node: %v", err)
	}
	if updated.Name != newName {
		t.Fatalf("updated name = %q, want %q", updated.Name, newName)
	}

	dstPath, err := svc.ResolveAbsPath(dir.ID)
	if err != nil {
		t.Fatalf("resolve dst path: %v", err)
	}
	if got := modeOf(t, dstPath); got != 0o750 {
		t.Fatalf("dst dir mode = %o, want %o", got, 0o750)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub")); got != 0o750 {
		t.Fatalf("dst sub dir mode = %o, want %o", got, 0o750)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub", "x.txt")); got != 0o640 {
		t.Fatalf("dst file mode = %o, want %o", got, 0o640)
	}
}

func TestUpdateNodePatchCanDisableRecursiveOwnership(t *testing.T) {
	svc, cleanup := newServiceForOwnershipTest(t)
	defer cleanup()

	root := svc.ListRoot()[0].ID
	dir, err := svc.CreateChild(root, "nr-src", true, nil)
	if err != nil {
		t.Fatalf("create nr-src dir: %v", err)
	}
	sub, err := svc.CreateChild(dir.ID, "sub", true, nil)
	if err != nil {
		t.Fatalf("create sub dir: %v", err)
	}
	if _, err := svc.CreateChild(sub.ID, "x.txt", false, nil); err != nil {
		t.Fatalf("create file x.txt: %v", err)
	}

	updated, err := svc.UpdateNode(dir.ID, nil, &domain.Ownership{
		Mode:    "600",
		DirMode: "700",
	}, false)
	if err != nil {
		t.Fatalf("patch update node non-recursive: %v", err)
	}

	dstPath, err := svc.ResolveAbsPath(updated.ID)
	if err != nil {
		t.Fatalf("resolve dst path: %v", err)
	}
	if got := modeOf(t, dstPath); got != 0o700 {
		t.Fatalf("root dir mode = %o, want %o", got, 0o700)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub")); got != 0o755 {
		t.Fatalf("sub dir mode = %o, want %o", got, 0o755)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub", "x.txt")); got != 0o644 {
		t.Fatalf("file mode = %o, want %o", got, 0o644)
	}
}

func TestTransferCanDisableRecursiveOwnership(t *testing.T) {
	svc, cleanup := newServiceForOwnershipTest(t)
	defer cleanup()

	root := svc.ListRoot()[0].ID
	src, err := svc.CreateChild(root, "src-nr", true, nil)
	if err != nil {
		t.Fatalf("create src dir: %v", err)
	}
	sub, err := svc.CreateChild(src.ID, "sub", true, nil)
	if err != nil {
		t.Fatalf("create sub dir: %v", err)
	}
	if _, err := svc.CreateChild(sub.ID, "x.txt", false, nil); err != nil {
		t.Fatalf("create file x.txt: %v", err)
	}

	recursive := false
	out, err := svc.Transfer(domain.TransferRequest{
		Op:             "copy",
		SourceID:       src.ID,
		TargetParentID: root,
		TargetName:     "dst-nr",
		OnConflict:     "error",
		Ownership: &domain.Ownership{
			Mode:    "600",
			DirMode: "700",
		},
		RecursiveOwnership: &recursive,
	})
	if err != nil {
		t.Fatalf("transfer non-recursive: %v", err)
	}

	dstPath, err := svc.ResolveAbsPath(out.ID)
	if err != nil {
		t.Fatalf("resolve dst path: %v", err)
	}
	if got := modeOf(t, dstPath); got != 0o700 {
		t.Fatalf("root dir mode = %o, want %o", got, 0o700)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub")); got != 0o755 {
		t.Fatalf("sub dir mode = %o, want %o", got, 0o755)
	}
	if got := modeOf(t, filepath.Join(dstPath, "sub", "x.txt")); got != 0o644 {
		t.Fatalf("file mode = %o, want %o", got, 0o644)
	}
}

func TestMissingOwnershipInheritsFromParentForDirsAndFiles(t *testing.T) {
	svc, cleanup := newServiceForOwnershipTest(t)
	defer cleanup()

	root := svc.ListRoot()[0].ID
	if _, err := svc.UpdateNode(root, nil, &domain.Ownership{
		Mode:    "640",
		DirMode: "750",
	}, true); err != nil {
		t.Fatalf("update root ownership: %v", err)
	}

	dir, err := svc.CreateChild(root, "inherited-dir", true, nil)
	if err != nil {
		t.Fatalf("create inherited dir: %v", err)
	}
	dirPath, err := svc.ResolveAbsPath(dir.ID)
	if err != nil {
		t.Fatalf("resolve dir path: %v", err)
	}
	if got := modeOf(t, dirPath); got != 0o750 {
		t.Fatalf("dir mode = %o, want %o", got, 0o750)
	}

	file, err := svc.CreateChild(root, "inherited-file.txt", false, nil)
	if err != nil {
		t.Fatalf("create inherited file: %v", err)
	}
	filePath, err := svc.ResolveAbsPath(file.ID)
	if err != nil {
		t.Fatalf("resolve file path: %v", err)
	}
	if got := modeOf(t, filePath); got != 0o640 {
		t.Fatalf("file mode = %o, want %o", got, 0o640)
	}

	nested, err := svc.MkdirRelative(root, "a/b/c", true, nil, domain.ConflictError)
	if err != nil {
		t.Fatalf("mkdir relative: %v", err)
	}
	nestedPath, err := svc.ResolveAbsPath(nested.ID)
	if err != nil {
		t.Fatalf("resolve nested path: %v", err)
	}
	if got := modeOf(t, nestedPath); got != 0o750 {
		t.Fatalf("nested dir mode = %o, want %o", got, 0o750)
	}
}

func TestMkdirRelativeAppliesOwnershipToCreatedChain(t *testing.T) {
	svc, cleanup := newServiceForOwnershipTest(t)
	defer cleanup()

	root := svc.ListRoot()[0].ID
	created, err := svc.MkdirRelative(root, "x/y/z", true, &domain.Ownership{
		Mode:    "600",
		DirMode: "700",
	}, domain.ConflictError)
	if err != nil {
		t.Fatalf("mkdir relative: %v", err)
	}
	createdPath, err := svc.ResolveAbsPath(created.ID)
	if err != nil {
		t.Fatalf("resolve created path: %v", err)
	}
	rootAbs, err := svc.ResolveAbsPath(root)
	if err != nil {
		t.Fatalf("resolve root path: %v", err)
	}

	if got := modeOf(t, filepath.Join(rootAbs, "x")); got != 0o700 {
		t.Fatalf("x mode = %o, want %o", got, 0o700)
	}
	if got := modeOf(t, filepath.Join(rootAbs, "x", "y")); got != 0o700 {
		t.Fatalf("x/y mode = %o, want %o", got, 0o700)
	}
	if got := modeOf(t, createdPath); got != 0o700 {
		t.Fatalf("x/y/z mode = %o, want %o", got, 0o700)
	}
}

func ptrInt(v int) *int { return &v }
