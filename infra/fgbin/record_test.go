package fgbin

import (
	"bytes"
	"testing"
)

func TestEntityRoundTrip(t *testing.T) {
	var id [16]byte
	var parent [16]byte
	copy(id[:], []byte("1234567890abcdef"))
	copy(parent[:], []byte("fedcba0987654321"))
	in := Entity{
		ID:       id,
		ParentID: parent,
		IsDir:    false,
		Size:     42,
		MtimeNs:  123456789,
		UID:      1000,
		GID:      1000,
		Mode:     0o644,
		Device:   42,
		Inode:    9001,
		Nlink:    1,
		Name:     "a.txt",
		MimeType: "text/plain",
		Extensions: []Extension{
			{FieldID: 4, Value: []byte{4}},
			{FieldID: 1, Value: []byte("exif")},
		},
	}
	blob, err := EncodeEntity(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeEntity(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != in.ID || out.ParentID != in.ParentID {
		t.Fatalf("id mismatch")
	}
	if out.Name != in.Name || out.MimeType != in.MimeType {
		t.Fatalf("string mismatch")
	}
	if out.Size != in.Size || out.MtimeNs != in.MtimeNs || out.Mode != in.Mode {
		t.Fatalf("numeric mismatch")
	}
	if out.Device != in.Device || out.Inode != in.Inode || out.Nlink != in.Nlink {
		t.Fatalf("inode identity mismatch: got dev=%d ino=%d nlink=%d", out.Device, out.Inode, out.Nlink)
	}
	if len(out.Extensions) != 2 {
		t.Fatalf("ext count=%d, want 2", len(out.Extensions))
	}
	if out.Extensions[0].FieldID != 1 || out.Extensions[1].FieldID != 4 {
		t.Fatalf("extensions not canonicalized by field id")
	}
}

func TestDecodeEntityRejectsDuplicateExtensions(t *testing.T) {
	var id [16]byte
	blob, err := EncodeEntity(Entity{
		ID:       id,
		ParentID: id,
		Name:     "x",
		Extensions: []Extension{
			{FieldID: 1, Value: []byte("a")},
			{FieldID: 1, Value: []byte("b")},
		},
	})
	if err == nil {
		t.Fatalf("expected duplicate ext encode error")
	}
	if !bytes.Equal(blob, nil) {
		t.Fatalf("blob must be nil on error")
	}
}

func TestEntityRoundTripWithS3Extensions(t *testing.T) {
	var id [16]byte
	copy(id[:], []byte("0123456789abcdef"))
	in := Entity{
		ID:       id,
		ParentID: id,
		Size:     1024,
		MtimeNs:  1,
		Name:     "obj.bin",
		MimeType: "application/octet-stream",
		Extensions: []Extension{
			{FieldID: FieldEXIF, Value: []byte(`{"k":"v"}`)},
			{FieldID: FieldETagMD5, Value: []byte("d41d8cd98f00b204e9800998ecf8427e")},
			{FieldID: FieldMultipartETag, Value: []byte("abc-3")},
			{FieldID: FieldContentType, Value: []byte("image/png")},
			{FieldID: FieldContentEncoding, Value: []byte("gzip")},
			{FieldID: FieldContentDisposition, Value: []byte(`attachment; filename="x.bin"`)},
			{FieldID: FieldS3UserMetadata, Value: []byte("user-meta-blob")},
		},
	}
	blob, err := EncodeEntity(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeEntity(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Extensions must round-trip in canonical (FieldID-ascending) order.
	if len(out.Extensions) != 7 {
		t.Fatalf("ext count=%d, want 7", len(out.Extensions))
	}
	wantOrder := []uint16{
		FieldEXIF, FieldETagMD5, FieldMultipartETag, FieldContentType,
		FieldContentEncoding, FieldContentDisposition, FieldS3UserMetadata,
	}
	for i, ext := range out.Extensions {
		if ext.FieldID != wantOrder[i] {
			t.Fatalf("ext[%d].FieldID=%d, want %d", i, ext.FieldID, wantOrder[i])
		}
	}
	if got, _ := ExtensionByID(out.Extensions, FieldETagMD5); string(got) != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Fatalf("ETagMD5 round-trip: got %q", got)
	}
	if got, _ := ExtensionByID(out.Extensions, FieldMultipartETag); string(got) != "abc-3" {
		t.Fatalf("MultipartETag round-trip: got %q", got)
	}
}

// TestEntityWithoutS3ExtensionsDecodes verifies that entity records
// emitted before the S3 schema additions still decode cleanly. This
// is the forward-compat property the codec relies on: encoding adds
// extensions only when set, so legacy rows produce minimal records
// that any future decoder reads as zero-valued extra fields.
func TestEntityWithoutS3ExtensionsDecodes(t *testing.T) {
	var id [16]byte
	in := Entity{
		ID:       id,
		ParentID: id,
		Size:     0,
		Name:     "legacy.txt",
		// No extensions at all — pre-schema-bump record shape.
	}
	blob, err := EncodeEntity(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeEntity(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Extensions) != 0 {
		t.Fatalf("ext count=%d, want 0", len(out.Extensions))
	}
	if got, ok := ExtensionByID(out.Extensions, FieldETagMD5); ok {
		t.Fatalf("FieldETagMD5 unexpectedly present in legacy record: %q", got)
	}
}

func TestChildRoundTrip(t *testing.T) {
	var id [16]byte
	copy(id[:], []byte("1234567890abcdef"))
	in := Child{ID: id, IsDir: true, Size: 7, MtimeNs: 9, Name: "dir"}
	blob, err := EncodeChild(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeChild(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != in.ID || out.Name != in.Name || out.IsDir != in.IsDir || out.Size != in.Size || out.MtimeNs != in.MtimeNs {
		t.Fatalf("child mismatch")
	}
}
