// Package object defines mygit's object model and the on-the-wire framing that
// turns a typed payload into the bytes that get hashed and stored.
//
// Every object is serialized identically:
//
//	<type> SP <size> NUL <payload>
//
// The object ID is the SHA-1 digest of that entire framed byte sequence,
// header included. Three consequences follow, and they are the whole reason
// the header exists:
//
//   - Including the type partitions the hash space, so a blob and a tree with
//     identical payloads receive different IDs and cannot alias each other.
//   - The decimal size makes the frame self-describing. A reader knows the
//     payload length before reading it, so it can size buffers exactly and
//     detect truncation. In a packfile, where objects are concatenated with no
//     file boundaries, the length is the only thing marking where one ends.
//   - NUL is the delimiter because the header is ASCII and the payload may be
//     arbitrary binary. NUL cannot occur in "<type> <size>", so the split needs
//     no escaping and admits no ambiguity.
package object

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"strconv"
)

// Type names the four kinds of object Git stores. mygit implements three;
// annotated tags are omitted as they add no new structural ideas.
type Type string

const (
	TypeBlob   Type = "blob"   // file contents, with no name and no metadata
	TypeTree   Type = "tree"   // a directory: names to (mode, OID) pairs
	TypeCommit Type = "commit" // a root tree, parent pointers, and metadata
)

// Valid reports whether t is a type mygit knows how to store.
func (t Type) Valid() bool {
	switch t {
	case TypeBlob, TypeTree, TypeCommit:
		return true
	}
	return false
}

// Serialize frames payload as "<type> <size>\x00<payload>".
//
// The returned bytes are exactly what gets hashed and, after compression,
// exactly what lands on disk.
func Serialize(t Type, payload []byte) []byte {
	header := t.String() + " " + strconv.Itoa(len(payload)) + "\x00"
	framed := make([]byte, 0, len(header)+len(payload))
	framed = append(framed, header...)
	framed = append(framed, payload...)
	return framed
}

// String makes Type printable and keeps the header construction above readable.
func (t Type) String() string { return string(t) }

// Hash returns the object ID of an already-serialized object.
func Hash(serialized []byte) OID { return sha1.Sum(serialized) }

// HashPayload frames a payload and returns its object ID without storing it.
// Hashing is a pure function of the content; persistence is a separate concern
// handled by the store package.
func HashPayload(t Type, payload []byte) OID { return Hash(Serialize(t, payload)) }

// Deserialize splits a framed object back into its type and payload.
//
// It is deliberately strict. Every rejected input here is a corruption that
// would otherwise surface much later as an inscrutable failure deep in tree or
// commit parsing, so the checks pay for themselves.
func Deserialize(serialized []byte) (Type, []byte, error) {
	nul := bytes.IndexByte(serialized, 0)
	if nul < 0 {
		return "", nil, fmt.Errorf("malformed object: header has no NUL terminator")
	}
	header, payload := serialized[:nul], serialized[nul+1:]

	sp := bytes.IndexByte(header, ' ')
	if sp < 0 {
		return "", nil, fmt.Errorf("malformed object header %q: no space separator", header)
	}

	typ := Type(header[:sp])
	if !typ.Valid() {
		return "", nil, fmt.Errorf("unknown object type %q", header[:sp])
	}

	size, err := parseSize(header[sp+1:])
	if err != nil {
		return "", nil, fmt.Errorf("malformed object header %q: %w", header, err)
	}
	if size != len(payload) {
		return "", nil, fmt.Errorf("object size mismatch: header claims %d bytes, payload has %d", size, len(payload))
	}
	return typ, payload, nil
}

// parseSize accepts only a bare run of decimal digits. strconv would also
// accept "+11" and "-11", which no correct writer emits; permitting them would
// let two different byte sequences describe the same object.
func parseSize(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("empty size field")
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("size field %q is not decimal", b)
		}
	}
	n, err := strconv.Atoi(string(b))
	if err != nil {
		return 0, fmt.Errorf("size field %q out of range", b)
	}
	return n, nil
}
