# mygit

A from-scratch reimplementation of Git's core in Go, built to understand
content-addressable storage, Merkle DAGs, and the storage engine underneath
version control. No Git libraries — object framing, hashing, compression, and
the on-disk format are all implemented directly.

Objects are byte-compatible with real Git. An object written by `mygit` can be
copied into a `.git/objects` directory and read by `git cat-file`, and passes
`git fsck --strict`.

## Status

| Phase | Command | State |
|---|---|---|
| 1 | `init` | done |
| 2 | `hash-object` | done |
| 3 | `cat-file` | done |
| 4 | `add`, `ls-files` (index) | done |
| 5 | `write-tree` (tree objects) | done |
| 6 | `commit` | next |
| 7 | `log` | |
| 8 | `checkout` | |
| 9–10 | `branch`, branch switching | |
| 11 | `merge` (three-way) | |

## Try it

```bash
go build -o mygit .

mkdir demo && cd demo
../mygit init
printf 'hello world' > file.txt

../mygit hash-object file.txt        # 95d09f2b... (no side effects)
../mygit hash-object -w file.txt     # same id, now stored
../mygit cat-file -p 95d09f2b...     # hello world

git hash-object file.txt             # identical id
```

## Architecture

```
main.go                  process boundary only: argv, streams, exit codes
internal/
  object/                object model, framing, and file modes; hashing is pure
  store/                 loose-object database: shard, compress, write, verify
  index/                 the staging area: binary format, stat cache, checksum
  fsutil/                atomic write-temp-fsync-rename, shared by mutable files
  repository/            .mygit layout, creation, and upward discovery
  cli/                   subcommand dispatch and output formatting
```

The dependency graph is acyclic and points one way: `cli → repository → store →
object`, with `index` and `fsutil` as leaf utilities. Nothing below `cli` knows
a terminal exists, which is what makes the storage engine testable without a
shell and reusable behind a future server.

## On-disk format

Every object is framed before hashing:

```
<type> SP <size> NUL <payload>
```

The object ID is the SHA-1 of that whole frame. The type is inside the digest
so a blob and a tree with identical payloads cannot alias. The size makes the
frame self-describing, which is what lets objects be concatenated into a
packfile with no delimiters. NUL separates ASCII header from binary payload
without escaping.

Storage is `objects/ab/cdef...` — two hex characters of directory sharding, so
256 evenly-filled buckets instead of one directory with a million entries.
Files hold the zlib-compressed frame. Compression happens *after* hashing, so
identity never depends on a storage-layer choice.

Writes are temp-file, fsync, rename. Rename is atomic within a filesystem, so a
crash can never leave behind a file whose name asserts a hash its contents do
not satisfy. Reads re-hash and compare, which is how bit rot gets caught
instead of silently propagated into a checkout.

## The index (staging area)

`add` hashes each file into a blob, stores it, and records an entry — path,
mode, blob ID, and a stat cache (mtime + size) — in `.mygit/index`. The index
is a full, flat, sorted snapshot of the *proposed next tree*, not a list of
changes: commit will serialize it into tree objects verbatim. The stat cache is
what will let `status` cost O(files) stat calls instead of O(bytes) of hashing.

The on-disk format is modeled on Git's index v2 (`DIRC` magic, version, count,
8-byte-aligned entries, SHA-1 trailer) but is deliberately *not* byte-compatible
— the index never leaves the repository, so unlike objects and refs it has no
interchange contract to honor. `ls-files -s` mirrors `git ls-files --stage`, and
for real source files it prints identical modes, blob IDs, and paths.

## Trees and the Merkle DAG

`write-tree` converts the flat index into nested tree objects and prints the
root ID. A tree row is `<octal mode> SP <name> NUL <20 raw bytes of OID>` —
note the ID is raw binary, not hex, and a directory's mode is `40000` with no
leading zero (`cat-file -p` pads it to `040000` for display only).

Entries are sorted with directories compared as if their names ended in `/`,
so a file `src.txt` sorts *before* a directory `src` — `.` is 0x2E, `/` is
0x2F. This is the usual reason a hand-built tree fails to match Git's hash.

Because a tree embeds its children's IDs, and those IDs hash their children's
full content, the root hash fingerprints the entire source tree. Editing one
deep file rewrites only the trees along its path to the root; every untouched
directory keeps its exact ID and is reused:

```
edit src/util/helper.go   ->  new blob, new util/, new src/, new root
                              README.md blob and all other trees: unchanged
                              7 objects on disk -> 11 (exactly 4 new)
```

Two directories with identical contents are literally the same tree object,
stored once — deduplication applies to structure, not just file content.

## Tests

```bash
go test ./...
```

Object IDs are pinned against values produced by real `git hash-object`, so a
regression in framing fails loudly rather than silently forking the format.
