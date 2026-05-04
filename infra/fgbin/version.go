package fgbin

import "encoding/binary"

// recordVersionRecordV1 is the current VersionMeta record format. Records
// describe one frozen state of a file in the per-file versioning subsystem.
const (
	versionRecordVersionV1 byte = 1

	recordTypeVersion byte = 3

	flagVersionPinned byte = 0x01

	// versionMinLenV1 is the byte length of the fixed-width portion of a
	// V1 version record before the variable-length Label and MountName.
	versionMinLenV1 = 4 + 16 + 16 + 8 + 8 + 4 + 8 + 2 + 2

	// MaxVersionLabelBytes caps the on-wire Label size. The same cap is
	// enforced at the API layer so the encoded record stays well below
	// any reasonable index value-size limits.
	MaxVersionLabelBytes = 2048

	// MaxVersionMountNameBytes caps the on-wire MountName size. Mount
	// names are operator-controlled and short (e.g. "data"), so 256 is
	// generous.
	MaxVersionMountNameBytes = 256
)

// Version is the binary record representation of a single file version.
type Version struct {
	VersionID [16]byte
	FileID    [16]byte
	Timestamp int64 // unix milliseconds
	Size      int64
	Mode      uint32
	DeletedAt int64 // 0 while file lives; unix-ms when entered grace
	Pinned    bool
	Label     []byte // opaque, ≤ MaxVersionLabelBytes
	MountName []byte // mount-name bytes, ≤ MaxVersionMountNameBytes
}

// EncodeVersion serializes a Version into its binary record format.
//
// Layout:
//
//	[0]      recordVersionRecordV1
//	[1]      recordTypeVersion
//	[2]      flags (bit 0 = pinned)
//	[3]      reserved (must be 0)
//	[4:20]   VersionID
//	[20:36]  FileID
//	[36:44]  Timestamp (int64 LE)
//	[44:52]  Size      (int64 LE)
//	[52:56]  Mode      (uint32 LE)
//	[56:64]  DeletedAt (int64 LE)
//	[64:66]  LabelLen     (uint16 LE)
//	[66:..]  Label bytes
//	[..:..]  MountNameLen (uint16 LE)
//	[..:..]  MountName bytes
func EncodeVersion(v Version) ([]byte, error) {
	if len(v.Label) > MaxVersionLabelBytes {
		return nil, ErrExtensionTooLong
	}
	if len(v.MountName) > MaxVersionMountNameBytes {
		return nil, ErrExtensionTooLong
	}
	out := make([]byte, versionMinLenV1+len(v.Label)+len(v.MountName))
	pos := 0

	out[pos] = versionRecordVersionV1
	out[pos+1] = recordTypeVersion
	if v.Pinned {
		out[pos+2] = flagVersionPinned
	}
	out[pos+3] = 0
	pos += 4

	copy(out[pos:pos+16], v.VersionID[:])
	pos += 16
	copy(out[pos:pos+16], v.FileID[:])
	pos += 16
	binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(v.Timestamp))
	pos += 8
	binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(v.Size))
	pos += 8
	binary.LittleEndian.PutUint32(out[pos:pos+4], v.Mode)
	pos += 4
	binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(v.DeletedAt))
	pos += 8
	binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(len(v.Label)))
	pos += 2
	copy(out[pos:pos+len(v.Label)], v.Label)
	pos += len(v.Label)
	binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(len(v.MountName)))
	pos += 2
	copy(out[pos:pos+len(v.MountName)], v.MountName)
	return out, nil
}

// DecodeVersion deserializes a Version from its binary record format.
func DecodeVersion(in []byte) (Version, error) {
	var out Version
	if len(in) < versionMinLenV1 {
		return out, ErrInvalidRecord
	}
	if in[0] != versionRecordVersionV1 {
		return out, ErrUnsupportedVer
	}
	if in[1] != recordTypeVersion {
		return out, ErrUnknownType
	}
	if in[2]&^flagVersionPinned != 0 {
		return out, ErrInvalidRecord
	}
	if in[3] != 0 {
		return out, ErrInvalidRecord
	}
	out.Pinned = (in[2] & flagVersionPinned) != 0

	pos := 4
	copy(out.VersionID[:], in[pos:pos+16])
	pos += 16
	copy(out.FileID[:], in[pos:pos+16])
	pos += 16
	out.Timestamp = int64(binary.LittleEndian.Uint64(in[pos : pos+8]))
	pos += 8
	out.Size = int64(binary.LittleEndian.Uint64(in[pos : pos+8]))
	pos += 8
	out.Mode = binary.LittleEndian.Uint32(in[pos : pos+4])
	pos += 4
	out.DeletedAt = int64(binary.LittleEndian.Uint64(in[pos : pos+8]))
	pos += 8
	labelLen := int(binary.LittleEndian.Uint16(in[pos : pos+2]))
	pos += 2
	if labelLen > MaxVersionLabelBytes {
		return out, ErrInvalidRecord
	}
	if pos+labelLen+2 > len(in) {
		return out, ErrInvalidRecord
	}
	if labelLen > 0 {
		out.Label = make([]byte, labelLen)
		copy(out.Label, in[pos:pos+labelLen])
	}
	pos += labelLen
	mountLen := int(binary.LittleEndian.Uint16(in[pos : pos+2]))
	pos += 2
	if mountLen > MaxVersionMountNameBytes {
		return out, ErrInvalidRecord
	}
	if pos+mountLen != len(in) {
		return out, ErrInvalidRecord
	}
	if mountLen > 0 {
		out.MountName = make([]byte, mountLen)
		copy(out.MountName, in[pos:pos+mountLen])
	}
	return out, nil
}
