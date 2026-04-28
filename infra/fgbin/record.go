package fgbin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

const (
	recordVersionV1 byte = 1

	recordTypeEntity byte = 1
	recordTypeChild  byte = 2

	flagIsDir byte = 0x01
)

const (
	FieldEXIF uint16 = 1
)

var (
	ErrInvalidRecord    = errors.New("invalid record")
	ErrUnsupportedVer   = errors.New("unsupported record version")
	ErrUnknownType      = errors.New("unknown record type")
	ErrNonCanonicalExt  = errors.New("non-canonical extensions")
	ErrExtensionTooLong = errors.New("extension value too long")
)

// Extension is an optional typed payload appended to an entity record.
type Extension struct {
	FieldID uint16
	Value   []byte
}

// Entity is the binary record representation of a file or directory.
type Entity struct {
	ID         [16]byte
	ParentID   [16]byte
	IsDir      bool
	Size       int64
	MtimeNs    int64
	UID        uint32
	GID        uint32
	Mode       uint32
	Name       string
	MimeType   string
	Extensions []Extension
}

// Child is the compact binary record for a directory listing entry.
type Child struct {
	ID      [16]byte
	IsDir   bool
	Size    int64
	MtimeNs int64
	Name    string
}

// EncodeEntity serializes an Entity into its binary record format.
func EncodeEntity(e Entity) ([]byte, error) {
	if len(e.Name) > 0xFFFF || len(e.MimeType) > 0xFFFF || len(e.Extensions) > 0xFFFF {
		return nil, ErrInvalidRecord
	}

	ext := append([]Extension(nil), e.Extensions...)
	sort.Slice(ext, func(i, j int) bool { return ext[i].FieldID < ext[j].FieldID })

	extBytes := 0
	for i := range ext {
		if i > 0 && ext[i].FieldID == ext[i-1].FieldID {
			return nil, ErrNonCanonicalExt
		}
		if len(ext[i].Value) > int(^uint32(0)) {
			return nil, ErrExtensionTooLong
		}
		extBytes += 6 + len(ext[i].Value)
	}

	total := 4 + 16 + 16 + 8 + 8 + 4 + 4 + 4 + 2 + len(e.Name) + 2 + len(e.MimeType) + 2 + extBytes
	out := make([]byte, total)
	pos := 0

	out[pos] = recordVersionV1
	out[pos+1] = recordTypeEntity
	if e.IsDir {
		out[pos+2] = flagIsDir
	}
	out[pos+3] = 0
	pos += 4

	copy(out[pos:pos+16], e.ID[:])
	pos += 16
	copy(out[pos:pos+16], e.ParentID[:])
	pos += 16
	binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(e.Size))
	pos += 8
	binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(e.MtimeNs))
	pos += 8
	binary.LittleEndian.PutUint32(out[pos:pos+4], e.UID)
	pos += 4
	binary.LittleEndian.PutUint32(out[pos:pos+4], e.GID)
	pos += 4
	binary.LittleEndian.PutUint32(out[pos:pos+4], e.Mode)
	pos += 4

	binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(len(e.Name)))
	pos += 2
	copy(out[pos:pos+len(e.Name)], e.Name)
	pos += len(e.Name)

	binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(len(e.MimeType)))
	pos += 2
	copy(out[pos:pos+len(e.MimeType)], e.MimeType)
	pos += len(e.MimeType)

	binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(len(ext)))
	pos += 2
	for _, ex := range ext {
		binary.LittleEndian.PutUint16(out[pos:pos+2], ex.FieldID)
		pos += 2
		binary.LittleEndian.PutUint32(out[pos:pos+4], uint32(len(ex.Value)))
		pos += 4
		copy(out[pos:pos+len(ex.Value)], ex.Value)
		pos += len(ex.Value)
	}
	return out, nil
}

// DecodeEntity deserializes an Entity from its binary record format.
func DecodeEntity(in []byte) (Entity, error) {
	var out Entity
	if len(in) < 70 {
		return out, ErrInvalidRecord
	}
	if in[0] != recordVersionV1 {
		return out, ErrUnsupportedVer
	}
	if in[1] != recordTypeEntity {
		return out, ErrUnknownType
	}
	if in[2]&^flagIsDir != 0 {
		return out, ErrInvalidRecord
	}
	out.IsDir = (in[2] & flagIsDir) != 0
	if in[3] != 0 {
		return out, ErrInvalidRecord
	}

	pos := 4
	copy(out.ID[:], in[pos:pos+16])
	pos += 16
	copy(out.ParentID[:], in[pos:pos+16])
	pos += 16
	out.Size = int64(binary.LittleEndian.Uint64(in[pos : pos+8]))
	pos += 8
	out.MtimeNs = int64(binary.LittleEndian.Uint64(in[pos : pos+8]))
	pos += 8
	out.UID = binary.LittleEndian.Uint32(in[pos : pos+4])
	pos += 4
	out.GID = binary.LittleEndian.Uint32(in[pos : pos+4])
	pos += 4
	out.Mode = binary.LittleEndian.Uint32(in[pos : pos+4])
	pos += 4

	nameLen := int(binary.LittleEndian.Uint16(in[pos : pos+2]))
	pos += 2
	if pos+nameLen > len(in) {
		return out, ErrInvalidRecord
	}
	out.Name = string(in[pos : pos+nameLen])
	pos += nameLen

	if pos+2 > len(in) {
		return out, ErrInvalidRecord
	}
	mimeLen := int(binary.LittleEndian.Uint16(in[pos : pos+2]))
	pos += 2
	if pos+mimeLen > len(in) {
		return out, ErrInvalidRecord
	}
	out.MimeType = string(in[pos : pos+mimeLen])
	pos += mimeLen

	if pos+2 > len(in) {
		return out, ErrInvalidRecord
	}
	extCount := int(binary.LittleEndian.Uint16(in[pos : pos+2]))
	pos += 2

	out.Extensions = make([]Extension, 0, extCount)
	var prev uint16
	for i := 0; i < extCount; i++ {
		if pos+6 > len(in) {
			return out, ErrInvalidRecord
		}
		fieldID := binary.LittleEndian.Uint16(in[pos : pos+2])
		pos += 2
		valueLen := int(binary.LittleEndian.Uint32(in[pos : pos+4]))
		pos += 4
		if i > 0 && fieldID <= prev {
			return out, ErrNonCanonicalExt
		}
		prev = fieldID
		if valueLen < 0 || pos+valueLen > len(in) {
			return out, ErrInvalidRecord
		}
		value := append([]byte(nil), in[pos:pos+valueLen]...)
		pos += valueLen
		out.Extensions = append(out.Extensions, Extension{FieldID: fieldID, Value: value})
	}
	if pos != len(in) {
		return out, ErrInvalidRecord
	}
	return out, nil
}

// EncodeChild serializes a Child into its compact binary record format.
func EncodeChild(c Child) ([]byte, error) {
	if len(c.Name) > 0xFFFF {
		return nil, ErrInvalidRecord
	}
	total := 4 + 16 + 8 + 8 + 2 + len(c.Name)
	out := make([]byte, total)
	pos := 0
	out[pos] = recordVersionV1
	out[pos+1] = recordTypeChild
	if c.IsDir {
		out[pos+2] = flagIsDir
	}
	out[pos+3] = 0
	pos += 4

	copy(out[pos:pos+16], c.ID[:])
	pos += 16
	binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(c.Size))
	pos += 8
	binary.LittleEndian.PutUint64(out[pos:pos+8], uint64(c.MtimeNs))
	pos += 8
	binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(len(c.Name)))
	pos += 2
	copy(out[pos:pos+len(c.Name)], c.Name)
	return out, nil
}

// DecodeChild deserializes a Child from its binary record format.
func DecodeChild(in []byte) (Child, error) {
	var out Child
	if len(in) < 38 {
		return out, ErrInvalidRecord
	}
	if in[0] != recordVersionV1 {
		return out, ErrUnsupportedVer
	}
	if in[1] != recordTypeChild {
		return out, ErrUnknownType
	}
	if in[2]&^flagIsDir != 0 {
		return out, ErrInvalidRecord
	}
	out.IsDir = (in[2] & flagIsDir) != 0
	if in[3] != 0 {
		return out, ErrInvalidRecord
	}

	pos := 4
	copy(out.ID[:], in[pos:pos+16])
	pos += 16
	out.Size = int64(binary.LittleEndian.Uint64(in[pos : pos+8]))
	pos += 8
	out.MtimeNs = int64(binary.LittleEndian.Uint64(in[pos : pos+8]))
	pos += 8
	nameLen := int(binary.LittleEndian.Uint16(in[pos : pos+2]))
	pos += 2
	if pos+nameLen != len(in) {
		return out, ErrInvalidRecord
	}
	out.Name = string(in[pos : pos+nameLen])
	return out, nil
}

// ExtensionByID returns the value of the extension with the given field ID, if present.
func ExtensionByID(ext []Extension, fieldID uint16) ([]byte, bool) {
	for _, e := range ext {
		if e.FieldID == fieldID {
			return append([]byte(nil), e.Value...), true
		}
	}
	return nil, false
}

// MustValidRecordType returns an error if t is not a recognized record type byte.
func MustValidRecordType(t byte) error {
	switch t {
	case recordTypeEntity, recordTypeChild:
		return nil
	default:
		return fmt.Errorf("%w: %d", ErrUnknownType, t)
	}
}
