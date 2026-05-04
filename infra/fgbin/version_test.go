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

// TestVersionDecodesPreMountNameRecord pins the codec's
// backward compatibility contract: records written before MountName was
// added MUST still decode (with empty MountName) — otherwise an upgrade
// silently drops every existing version on disk.
//
// The byte string below is what the pre-MountName encoder would have
// produced for an empty-label record: 4-byte header + 16+16+8+8+4+8
// fixed fields + 2-byte zero label length, with no trailing bytes.
func TestVersionDecodesPreMountNameRecord(t *testing.T) {
	// Manually assemble a pre-MountName V1 record (66 bytes).
	rec := make([]byte, 4+16+16+8+8+4+8+2)
	rec[0] = versionRecordVersionV1
	rec[1] = recordTypeVersion
	// flags=0, reserved=0, IDs zero, ints zero, labelLen=0
	out, err := DecodeVersion(rec)
	if err != nil {
		t.Fatalf("decode pre-MountName record: %v", err)
	}
	if len(out.MountName) != 0 {
		t.Fatalf("MountName non-empty on legacy record: %q", out.MountName)
	}
}

// TestVersionEncodeOmitsMountTrailerWhenEmpty pins that the encoder
// produces a byte-identical record to the pre-MountName format when
// MountName is empty. This is what makes the round-trip with old
// records lossless.
func TestVersionEncodeOmitsMountTrailerWhenEmpty(t *testing.T) {
	blob, err := EncodeVersion(Version{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(blob) != versionMinLenV1 {
		t.Fatalf("empty-MountName record len=%d, want %d (no trailer)", len(blob), versionMinLenV1)
	}
}

func TestVersionRejectsUnknownFlagBit(t *testing.T) {
	blob, _ := EncodeVersion(Version{})
	blob[2] = 0x80
	if _, err := DecodeVersion(blob); err == nil {
		t.Fatalf("expected error for unknown flag bit")
	}
}
