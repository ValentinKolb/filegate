package fgbin

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionRoundTrip(t *testing.T) {
	var fid, vid [16]byte
	copy(fid[:], []byte("file-id-aaaaaaaa"))
	copy(vid[:], []byte("ver-id-bbbbbbbbb"))

	in := Version{
		VersionID: vid,
		FileID:    fid,
		Timestamp: 1_700_000_000_123,
		Size:      4096,
		Mode:      0o640,
		DeletedAt: 0,
		Pinned:    true,
		Label:     []byte(`{"author":"valentin","tag":"q3-release"}`),
	}
	blob, err := EncodeVersion(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeVersion(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.VersionID != in.VersionID || out.FileID != in.FileID {
		t.Fatalf("id mismatch")
	}
	if out.Timestamp != in.Timestamp || out.Size != in.Size || out.Mode != in.Mode {
		t.Fatalf("numeric mismatch")
	}
	if out.DeletedAt != in.DeletedAt || out.Pinned != in.Pinned {
		t.Fatalf("flag/timestamp mismatch")
	}
	if !bytes.Equal(out.Label, in.Label) {
		t.Fatalf("label mismatch: got %q, want %q", out.Label, in.Label)
	}
}

func TestVersionEmptyLabelDecodes(t *testing.T) {
	blob, err := EncodeVersion(Version{Pinned: false})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeVersion(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Label) != 0 {
		t.Fatalf("expected empty label, got %d bytes", len(out.Label))
	}
}

func TestVersionOrphanFlag(t *testing.T) {
	in := Version{DeletedAt: 1_700_000_000_555}
	blob, _ := EncodeVersion(in)
	out, err := DecodeVersion(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.DeletedAt != in.DeletedAt {
		t.Fatalf("DeletedAt round-trip: got %d, want %d", out.DeletedAt, in.DeletedAt)
	}
}

func TestVersionRejectsOversizedLabel(t *testing.T) {
	huge := strings.Repeat("x", MaxVersionLabelBytes+1)
	if _, err := EncodeVersion(Version{Label: []byte(huge)}); err == nil {
		t.Fatalf("expected error for label > %d bytes", MaxVersionLabelBytes)
	}
}

func TestVersionRejectsTruncatedRecord(t *testing.T) {
	blob, _ := EncodeVersion(Version{Label: []byte("hello")})
	if _, err := DecodeVersion(blob[:versionMinLenV1-1]); err == nil {
		t.Fatalf("expected error for truncated record")
	}
}

func TestVersionRejectsTrailingBytes(t *testing.T) {
	blob, _ := EncodeVersion(Version{Label: []byte("ok")})
	blob = append(blob, 0xFF)
	if _, err := DecodeVersion(blob); err == nil {
		t.Fatalf("expected error for trailing bytes")
	}
}

func TestVersionRejectsWrongType(t *testing.T) {
	blob, _ := EncodeVersion(Version{})
	blob[1] = recordTypeEntity
	if _, err := DecodeVersion(blob); err == nil {
		t.Fatalf("expected ErrUnknownType")
	}
}

func TestVersionRejectsUnsupportedVersionByte(t *testing.T) {
	blob, _ := EncodeVersion(Version{})
	blob[0] = 0xFF
	if _, err := DecodeVersion(blob); err == nil {
		t.Fatalf("expected ErrUnsupportedVer")
	}
}

func TestVersionRejectsUnknownFlagBit(t *testing.T) {
	blob, _ := EncodeVersion(Version{})
	blob[2] = 0x80
	if _, err := DecodeVersion(blob); err == nil {
		t.Fatalf("expected error for unknown flag bit")
	}
}
