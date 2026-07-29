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
| 6 | `commit`, `rev-parse` (refs) | done |
| 7 | `log` (DAG traversal) | done |
| 8 | `checkout` (worktree) | done |
| 9–10 | `branch`, `checkout -b` (switching) | done |
| 11 | `merge` (three-way) | next |

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
  refs/                  mutable pointers: HEAD, branches, symbolic refs
  graph/                 DAG traversal: priority-queue walk, visited set
  worktree/              objects to filesystem: plan, validate, apply
  fsutil/                atomic write-temp-fsync-rename, shared by mutable files
  repository/            .mygit layout, creation, and upward discovery
  cli/                   subcommand dispatch and output formatting
```

The dependency graph is acyclic and points one way: `cli → repository →
{store, refs} → object`, with `index` and `fsutil` as leaf utilities. Nothing
below `cli` knows a terminal exists, which is what makes the storage engine
testable without a shell and reusable behind a future server.

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

## Commits and refs

A commit is a root tree ID, zero or more parent IDs, an author and committer
signature, and a message. Nothing else — no diff, no file list, no changeset.
"What changed" is always computed by diffing against a parent, never stored.

Commits store their tree and parent IDs as 40-char hex, unlike trees which use
20 raw bytes: a commit is a text format meant to stay readable, a tree is a
dense binary record. Parent pointers run *backward*, which is what makes
commits immutable — appending a child never touches the parent.

Refs are the mutable layer over that immutable graph. A branch is one file
holding one object ID; creating a branch writes 41 bytes. HEAD adds a second
indirection by holding `ref: refs/heads/main`, so committing advances the
*branch* and never rewrites HEAD:

```
HEAD ──▶ refs/heads/main ──▶ commit C ──▶ commit B ──▶ commit A
                             (new commits move the branch, not HEAD)
```

Because a commit ID hashes the timestamp and identity too, commit dates are
injectable via `MYGIT_AUTHOR_DATE` / `GIT_AUTHOR_DATE` — which is what makes
the byte-exact comparisons against real Git in the tests possible.

## History traversal

`log` walks the commit DAG backwards from a starting commit. Two decisions in
[internal/graph](internal/graph/walk.go) carry the whole phase:

**A visited set is mandatory, not an optimization.** Each merge diamond doubles
the number of distinct paths from tip to root, so a walk without memory costs
O(2ⁿ) on history with n diamonds — the shape any repo with a long-lived branch
has. Marking commits when *enqueued* (not when emitted) makes it O(V + E). The
test builds 30 diamonds: >1 billion paths, 91 commits, 91 object reads.

**A priority queue, not a stack or a plain queue.** DFS would walk one branch
to the root before showing anything from the other, burying recent work under
ancient history. Ordering by committer date makes the walk a k-way merge of
sorted streams, costing O(V log V + E):

```
        base
       /    \
   old-1   recent-1     long+old branch     vs.   short+new branch
   old-2   recent-2
   old-3     |
       \    /
        merge          log shows: merge, recent-2, recent-1, old-3 … base
                       (base appears exactly once)
```

Cycles need no detection: a commit's ID hashes its parents' IDs, so a cycle
would require a commit to know its own hash before computing it.
Content-addressing makes cycles unrepresentable.

The caveat is honest — date order trusts wall clocks, so skew or rebases can
make a child look older than its parent. Real Git offers `--topo-order` for
that; mygit does not.

## Checkout

`checkout` runs the pipeline backwards — objects onto the filesystem — and
moves all three trees together: HEAD, the index, and the working tree. Leaving
any one behind produces a repository that lies about itself.

Writing objects was always safe, because objects are immutable and
content-addressed. Checkout overwrites and deletes real files, and a file the
user edited but never staged exists in exactly one place on Earth. So the
design is strictly two-phase — **plan, validate, then apply**:

```
BuildPlan   diff current vs target trees   (unchanged files skipped by OID)
Validate    reject if work would be lost   (no side effects yet)
Apply       delete, then write, then prune (only once proven safe)
```

Two distinct hazards are refused, and conflating them is the usual bug: a
*tracked* file with uncommitted edits (recoverable — an older version is in
history), and an *untracked* file in the way (unrecoverable — never hashed,
never stored). Deletions run before writes so a directory can become a file.
Emptied directories are pruned, since Git cannot represent an empty directory.

The index is a *cache* of what the working tree is believed to hold, so
`BuildPlan` also confirms each supposedly-correct file actually exists —
deleting a tracked file and checking out restores it, matching real Git.

## Branches

A branch is one file holding one object ID. Measured on a repo with 40 files
and 43 objects:

```
before creating branches:  43 objects, 207,109 bytes
after 5 branches:          43 objects, 207,109 bytes   (+205 bytes of refs)
```

Creating a branch copies no history, touches no files, and writes no objects —
it names a graph that already exists. That is the whole reason the operation
costing minutes in a server-side VCS costs nothing here: the cost of an
operation follows from how the data is represented.

`branch -d` refuses to delete an unmerged branch, and the check is a
reachability question — `graph.IsAncestor(tip, HEAD)`. The refusal matters
because deletion destroys *nothing*:

```
branch -D feature
→ commit object fcfc413 still on disk?  commit
→ reachable by name?                    unknown revision "feature"
```

Objects are immortal; *names* are not, and a name is the only practical way to
find anything. The commits are stranded, not deleted — which is worse, because
nothing looks broken afterwards. `IsAncestor` is also the foundation of the
merge base in Phase 11.

## Interoperability

A three-commit history authored entirely by `mygit` can be dropped into a real
`.git/objects` directory and read by Git unchanged — `git log` shows the
history, `git checkout` restores the files, and `git fsck --strict` reports no
errors. With identity and timestamps pinned, both tools produce **identical
commit hashes** for the same content.

## Tests

```bash
go test ./...
```

Object IDs are pinned against values produced by real `git hash-object`, so a
regression in framing fails loudly rather than silently forking the format.
