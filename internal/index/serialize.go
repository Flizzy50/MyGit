package index

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"mygit/internal/fsutil"
	"mygit/internal/object"
)

// The on-disk index is a binary format modeled on Git's index version 2, but
// deliberately not byte-compatible with it. That non-goal is itself a lesson:
// the index never travels between repositories. Objects and refs are Git's
// interchange format — they go over the wire and must match exactly — but the
// index is a private, local, rebuildable cache. Copying Git's structure teaches
// the real skills (fixed + variable-length binary records, network byte order,
// alignment, a checksum trailer) without chaining us to platform-specific stat
// fields that are zero on Windows anyway.
//
// Layout:
//
//	Header (12 bytes)
//	  "DIRC"                     magic, "dircache"
//	  uint32  version            = 2
//	  uint32  entry count
//
//	Entry (repeated, padded to a multiple of 8 bytes)
//	  uint32  mtime seconds
//	  uint32  mtime nanoseconds
//	  uint32  size (low 32 bits)
//	  uint32  mode
//	  [20]    object id
//	  uint16  flags (low 12 bits: name length, capped)
//	  bytes   path
//	  NUL     terminator, then NULs padding the entry to an 8-byte multiple
//
//	Trailer (20 bytes)
//	  SHA-1 over every preceding byte
//
// All integers are big-endian. Git calls this "network byte order" and uses it
// even for a local file, so that every Git format shares one convention and a
// hex dump reads the same on any machine. Each entry is padded so its own
// length is a multiple of 8, mirroring Git. Note this does not put entries on
// absolute 8-byte offsets — the header is 12 bytes, so the first entry sits at
// offset 12 — but keeping every entry length a multiple of 8 means every entry
// starts at a consistent offset modulo 8, which is what keeps the fixed 4-byte
// fields aligned when the index is memory-mapped.
const (
	indexMagic   = "DIRC"
	indexVersion = 2

	headerSize = 12
	// fixed per-entry bytes before the variable-length path: three stat words
	// (mtime sec, mtime nsec, size), the mode, the 20-byte OID, and 2 flag
	// bytes. Git's is 62 because it also stores ctime, dev, ino, uid, and gid.
	entryFixedSize = 4 + 4 + 4 + 4 + object.OIDSize + 2
	entryAlignment = 8
	trailerSize    = sha1.Size

	// The 16-bit flags field is partitioned exactly as Git partitions it:
	// the low 12 bits hold the name length, and bits 12-13 hold the merge
	// stage. Reserving those two bits from the start is why conflict support
	// needed no format change — the space was already there, which is a good
	// argument for copying a mature format rather than inventing one.
	nameMask   = 0x0FFF
	stageShift = 12
	stageMask  = 0x3
)

// Load reads and verifies the index at path.
//
// A missing file is not an error: it is the empty-index state of a freshly
// initialized repository, where nothing has been staged yet. Returning an empty
// index here means every caller can treat "no index" and "empty index"
// identically, instead of special-casing the first `add`.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading index: %w", err)
	}
	return Decode(data)
}

// Save encodes the index and writes it atomically, so an interrupted `add`
// leaves the previous index intact rather than a truncated one.
func (idx *Index) Save(path string) error {
	return fsutil.AtomicWriteFile(path, idx.Encode(), 0o644)
}

// Encode serializes the index to its on-disk bytes, trailer included.
func (idx *Index) Encode() []byte {
	entries := idx.Entries() // canonical sorted order; see Index.Entries
	var buf bytes.Buffer

	buf.WriteString(indexMagic)
	writeUint32(&buf, indexVersion)
	writeUint32(&buf, uint32(len(entries)))

	for _, e := range entries {
		writeUint32(&buf, e.MtimeSec)
		writeUint32(&buf, e.MtimeNsec)
		writeUint32(&buf, e.Size)
		writeUint32(&buf, uint32(e.Mode))
		buf.Write(e.OID[:])

		nameLen := len(e.Path)
		if nameLen > nameMask {
			nameLen = nameMask // the length is a hint; the NUL is the real terminator
		}
		writeUint16(&buf, uint16(e.Stage&stageMask)<<stageShift|uint16(nameLen))

		buf.WriteString(e.Path)

		// Pad from the entry start to a multiple of 8, always leaving at least
		// one NUL so the name stays terminated even when it is not truncated.
		padded := entryFixedSize + len(e.Path)
		pad := entryAlignment - padded%entryAlignment
		buf.Write(make([]byte, pad))
	}

	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])
	return buf.Bytes()
}

// Decode parses and verifies raw index bytes.
//
// Verification is strict for the same reason it is in the object store: a
// corrupt index misparsed silently would surface as a bogus commit much later.
// The trailer is checked first, because if the bytes are damaged there is no
// point trusting any field parsed from them.
func Decode(data []byte) (*Index, error) {
	if len(data) < headerSize+trailerSize {
		return nil, fmt.Errorf("index too short: %d bytes", len(data))
	}

	body, trailer := data[:len(data)-trailerSize], data[len(data)-trailerSize:]
	if sum := sha1.Sum(body); !bytes.Equal(sum[:], trailer) {
		return nil, errors.New("index checksum mismatch: file is corrupt")
	}

	if string(body[:4]) != indexMagic {
		return nil, fmt.Errorf("bad index magic %q", body[:4])
	}
	if v := binary.BigEndian.Uint32(body[4:8]); v != indexVersion {
		return nil, fmt.Errorf("unsupported index version %d", v)
	}
	count := binary.BigEndian.Uint32(body[8:12])

	idx := New()
	pos := headerSize
	for i := uint32(0); i < count; i++ {
		e, next, err := decodeEntry(body, pos)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		idx.Add(e)
		pos = next
	}

	// The count and the byte layout must agree. If parsing stopped short of the
	// trailer, or ran into it, the file is inconsistent even though the
	// checksum matched — the count field lied.
	if pos != len(body) {
		return nil, fmt.Errorf("index has trailing bytes: parsed to %d of %d", pos, len(body))
	}
	return idx, nil
}

func decodeEntry(body []byte, pos int) (*Entry, int, error) {
	if pos+entryFixedSize > len(body) {
		return nil, 0, errors.New("truncated fixed fields")
	}

	e := &Entry{
		MtimeSec:  binary.BigEndian.Uint32(body[pos : pos+4]),
		MtimeNsec: binary.BigEndian.Uint32(body[pos+4 : pos+8]),
		Size:      binary.BigEndian.Uint32(body[pos+8 : pos+12]),
		Mode:      object.Mode(binary.BigEndian.Uint32(body[pos+12 : pos+16])),
	}
	copy(e.OID[:], body[pos+16:pos+16+object.OIDSize])

	flags := binary.BigEndian.Uint16(body[pos+16+object.OIDSize : pos+entryFixedSize])
	e.Stage = Stage(flags>>stageShift) & Stage(stageMask)

	// The stored name length is only a hint (and is capped), so the NUL
	// terminator is authoritative. Scanning for it also validates the record.
	nameStart := pos + entryFixedSize

	nul := bytes.IndexByte(body[nameStart:], 0)
	if nul < 0 {
		return nil, 0, errors.New("entry path is not NUL-terminated")
	}
	e.Path = string(body[nameStart : nameStart+nul])

	padded := entryFixedSize + len(e.Path)
	pad := entryAlignment - padded%entryAlignment
	next := nameStart + len(e.Path) + pad
	if next > len(body) {
		return nil, 0, errors.New("entry padding runs past end of index")
	}
	return e, next, nil
}

func writeUint32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func writeUint16(buf *bytes.Buffer, v uint16) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	buf.Write(b[:])
}
