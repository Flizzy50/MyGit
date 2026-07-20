package object

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
)

// OIDSize is the width in bytes of an object ID.
//
// Git is mid-migration from SHA-1 to SHA-256, so real Git carries an abstract
// object_id struct rather than a bare digest. Keeping the width behind a
// constant and the digest behind a named type means swapping hash functions
// touches this file and nothing else.
const OIDSize = sha1.Size

// OID identifies an object by the SHA-1 digest of its serialized form.
//
// It is an array rather than a slice or a string so that it is comparable with
// ==, usable as a map key, and copied by value. A hex string would be twice the
// size, allow malformed values to circulate, and let unrelated strings be
// passed where an object ID is required.
type OID [OIDSize]byte

// String returns the 40-character lowercase hex form used in output and paths.
func (o OID) String() string { return hex.EncodeToString(o[:]) }

// IsZero reports whether o is the null OID, which denotes "no object" — for
// example the parent of a root commit.
func (o OID) IsZero() bool { return o == OID{} }

// ParseOID decodes a full-length hexadecimal object ID.
//
// Abbreviated IDs are not accepted here: resolving a prefix requires scanning
// the object database, which is a store concern, not a parsing one.
func ParseOID(s string) (OID, error) {
	var o OID
	if len(s) != 2*OIDSize {
		return o, fmt.Errorf("invalid object id %q: want %d hex digits, got %d", s, 2*OIDSize, len(s))
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return o, fmt.Errorf("invalid object id %q: not hexadecimal", s)
	}
	copy(o[:], raw)
	return o, nil
}
