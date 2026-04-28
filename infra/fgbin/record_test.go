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
