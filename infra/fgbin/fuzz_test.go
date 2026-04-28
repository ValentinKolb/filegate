package fgbin

import (
	"encoding/binary"
	"testing"
)

func FuzzDecodeEntity(f *testing.F) {
	valid, err := EncodeEntity(Entity{
		Name:    "seed.txt",
		IsDir:   false,
		Size:    123,
		MtimeNs: 456,
	})
	if err == nil {
		f.Add(valid)
	}
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeEntity(data)
	})
}

func FuzzDecodeChild(f *testing.F) {
	valid, err := EncodeChild(Child{
		Name:    "child",
		IsDir:   true,
		Size:    1,
		MtimeNs: 2,
	})
	if err == nil {
		f.Add(valid)
	}
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeChild(data)
	})
}

func FuzzEntityRoundTrip(f *testing.F) {
	f.Add([]byte("name"), []byte("application/octet-stream"), false, int64(42), int64(99), uint32(1000), uint32(1000), uint32(0o644))
	f.Add([]byte("dir"), []byte(""), true, int64(0), int64(0), uint32(0), uint32(0), uint32(0o755))

	f.Fuzz(func(t *testing.T, rawName, rawMime []byte, isDir bool, size, mtime int64, uid, gid, mode uint32) {
		if len(rawName) > 1024 || len(rawMime) > 256 {
			t.Skip()
		}
		e := Entity{
			IsDir:    isDir,
			Size:     size,
			MtimeNs:  mtime,
			UID:      uid,
			GID:      gid,
			Mode:     mode,
			Name:     string(rawName),
			MimeType: string(rawMime),
		}
		payload, err := EncodeEntity(e)
		if err != nil {
			return
		}
		out, err := DecodeEntity(payload)
		if err != nil {
			t.Fatalf("decode failed after encode: %v", err)
		}
		if out.Name != e.Name || out.MimeType != e.MimeType || out.IsDir != e.IsDir || out.Size != e.Size || out.MtimeNs != e.MtimeNs {
			t.Fatalf("roundtrip mismatch")
		}
	})
}

func FuzzChildRoundTrip(f *testing.F) {
	f.Add([]byte("child"), false, int64(1), int64(2))
	f.Add([]byte("dir"), true, int64(0), int64(0))

	f.Fuzz(func(t *testing.T, rawName []byte, isDir bool, size, mtime int64) {
		if len(rawName) > 1024 {
			t.Skip()
		}
		c := Child{
			IsDir:   isDir,
			Size:    size,
			MtimeNs: mtime,
			Name:    string(rawName),
		}
		payload, err := EncodeChild(c)
		if err != nil {
			return
		}
		out, err := DecodeChild(payload)
		if err != nil {
			t.Fatalf("decode failed after encode: %v", err)
		}
		if out.Name != c.Name || out.IsDir != c.IsDir || out.Size != c.Size || out.MtimeNs != c.MtimeNs {
			t.Fatalf("roundtrip mismatch")
		}
	})
}

func FuzzMustValidRecordType(f *testing.F) {
	f.Add(byte(1))
	f.Add(byte(2))
	f.Add(byte(0))
	f.Add(byte(255))
	f.Fuzz(func(t *testing.T, b byte) {
		_ = MustValidRecordType(b)
	})
}

func FuzzDecodeEntityWithCorruptedLengths(f *testing.F) {
	base, err := EncodeEntity(Entity{Name: "a", MimeType: "b"})
	if err != nil {
		return
	}
	f.Add(base)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 70 {
			return
		}
		mut := append([]byte(nil), data...)
		// Overwrite name length with fuzzed bytes when possible.
		if len(mut) > 66 {
			binary.LittleEndian.PutUint16(mut[64:66], uint16(len(mut)))
		}
		_, _ = DecodeEntity(mut)
	})
}
