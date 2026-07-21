package object

import (
	"fmt"
	"io/fs"
	"strconv"
)

// Mode is a file mode as Git records it: a small set of discrete values, not
// arbitrary Unix permission bits.
//
// This is one of Git's deliberate reductions of the filesystem. A POSIX file
// has nine permission bits plus setuid/setgid/sticky; Git collapses all of that
// to "is it executable or not," because those are the only distinctions that
// affect how the file behaves when checked out. Recording full permissions
// would make the same source tree hash differently depending on each
// developer's umask, which would be absurd — so Git normalizes.
//
// The values are the classic Unix st_mode octal encodings, and they are shared
// verbatim between the index and tree objects. In a tree they are written as
// ASCII octal ("100644"); in the index they are a 32-bit integer. Defining the
// type here, rather than in index or tree, keeps both readers speaking the same
// language.
type Mode uint32

const (
	// ModeRegular is a normal, non-executable file: octal 100644.
	ModeRegular Mode = 0o100644
	// ModeExecutable is a file with the executable bit set: octal 100755.
	ModeExecutable Mode = 0o100755
	// ModeSymlink is a symbolic link; the blob's content is the link target.
	ModeSymlink Mode = 0o120000
	// ModeTree is a subdirectory. It appears only inside tree objects, never
	// in the index, since the index is a flat list of files.
	ModeTree Mode = 0o040000
)

// ModeFromFS derives a blob mode from a filesystem entry.
//
// The mapping is intentionally lossy: everything is a regular file unless the
// executable bit is set. On Windows the executable bit is not represented in
// the filesystem, so fs.FileMode never carries 0o111 and every file is recorded
// as ModeRegular — which is exactly how Git behaves on Windows, so mygit's trees
// stay stable across platforms instead of flip-flopping an exec bit.
func ModeFromFS(m fs.FileMode) Mode {
	switch {
	case m&fs.ModeSymlink != 0:
		return ModeSymlink
	case m&0o111 != 0:
		return ModeExecutable
	default:
		return ModeRegular
	}
}

// IsTree reports whether the mode denotes a subdirectory.
func (m Mode) IsTree() bool { return m == ModeTree }

// String formats the mode as Git writes it in tree objects: octal, no leading
// zero. Trees use "100644"; the directory mode prints as "40000", also matching
// Git, which drops the leading zero there too.
func (m Mode) String() string { return strconv.FormatUint(uint64(m), 8) }

// ParseMode reads an octal mode string as found in a tree object.
func ParseMode(s string) (Mode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid mode %q: %w", s, err)
	}
	return Mode(v), nil
}
