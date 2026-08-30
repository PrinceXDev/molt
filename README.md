# molt

**Finds the dependencies the Go standard library has already replaced, and rewrites the ones it can do safely.**

Zero Dependency Hackathon · Track A — Developer Tools & CLI · Go, standard library only

---

## Why this exists

The Go standard library keeps absorbing the packages everyone installs.

- Go 1.13 shipped `%w`, and `github.com/pkg/errors` became mostly redundant.
- Go 1.21 shipped `log/slog`, `slices`, `maps` and `cmp`.
- Go 1.22 shipped method and wildcard routing in `net/http.ServeMux`.
- Go 1.25 shipped `testing/synctest` and `sync.WaitGroup.Go`.
- Go 1.27 shipped `uuid` and graduated `encoding/json/v2`.

Almost nobody goes back and removes the old dependency, because doing it safely
means auditing which symbols you actually use and whether the stdlib equivalent
really matches. That is mechanical work, so a tool should do it.

> **Your `go.mod` is a fossil record of the last time you checked what the standard library could do.**

molt reads that record, tells you which rows are now dead weight, and applies the
ones it can prove are safe.

There is a second, smaller point. molt is a static-analysis tool, and every
static-analysis tool in the Go ecosystem imports `golang.org/x/tools/go/packages`
to load and inspect source. molt does not. It uses `go/parser`, `go/ast`,
`go/token` and `go/format` — which is to say, **this tool can exist with an empty
manifest because Go's standard library contains a Go parser.** That is not a
coincidence. That is what a good standard library is for.

## What it looks like

```
$ molt .

molt github.com/minio/minio

  Go files scanned   902
  Direct requires    95
  Indirect requires  164
  Modules in go.sum  355  3.7x your 95 direct requires

REMOVABLE molt can apply these in full

  github.com/mitchellh/go-homedir -> os
    stdlib since go1.12 · 1 symbol, 1 use, 1 file
    Dir

PARTLY REMOVABLE molt can apply these to some files

  github.com/google/uuid -> uuid
    stdlib since go1.27 · 6 symbols, 81 uses, 29 files
    molt can migrate 21 of 29 files now
    MustParse, New, NewRandom, NewString, Parse, UUID

NEEDS A HUMAN the replacement changes the shape of the code

  github.com/dustin/go-humanize -> strconv, fmt and time
    stdlib since go1.0 · 9 symbols, 275 uses, 53 files
    blocked by the replacement changes the shape of the code, not just its names

  1 removable · 2 partly removable · 4 need a human · 0 unused
```

Then, if you want it done:

```
$ molt -apply .
  rewrote internal/grid/connection.go
  rewrote cmd/object-handlers.go
  ...
Rewrote 23 files. Run go mod tidy to drop the requires, then go test ./... to confirm.
```

The edit is exactly what you would have typed:

```diff
 	"sync/atomic"
 	"time"
+	"uuid"
 
 	"github.com/gobwas/ws"
-	"github.com/google/uuid"
 	"github.com/minio/madmin-go/v3"
```

## Build and run

One command, no toolchain gymnastics:

```bash
go build -o molt ./cmd/molt
```

Or with make:

```bash
make build
```

Requires Go 1.25 or newer to build. The published binaries are built with Go
1.27.0; see [Reproducible build](#reproducible-build).

```bash
molt .                  # report on the current module
molt -v .               # add rationale, guidance and per-symbol detail
molt -diff .            # print the patch molt would apply, write nothing
molt -apply .           # rewrite files in place
molt -json .            # machine-readable report
molt -exit-code .       # exit 1 when there are findings, for CI
molt -corpus            # print the migration table molt knows
```

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | findings present — only with `-exit-code` |
| 2 | bad flags, or the path is not a directory |
| 3 | molt could not complete |

A plain `molt .` exits 0 even when it finds things. Reporting is not failing.
`-exit-code` is the opt-in for CI, following the `gofmt -l` and
`git diff --exit-code` convention.

## The safety model

molt edits source code, so the interesting design question is not what it can do
but what it refuses to do.

Every migration in the corpus is one of two kinds:

**Mechanical.** molt has verified that the replacement is behaviour-preserving at
every call site it permits, symbol by symbol. It will rewrite these.

**Advisory.** The migration is real, but it changes the shape of the code rather
than just its names — `logrus.WithFields(...)` to `slog` attributes, a
`gorilla/mux` route table to `ServeMux` patterns. molt explains it and leaves it
to you.

On top of that, molt declines to touch a file when:

- the package is dot-imported, so selectors cannot be attributed;
- the package's name is shadowed anywhere in the file by a variable, parameter,
  type or field;
- the file uses any symbol not in the verified replacement table;
- the rewritten file does not re-parse — molt parses its own output and refuses
  to write anything the parser rejects.

Eligibility is decided **per file**, not per module, because a project may use
one awkward symbol in one place and clean ones in eighty others. That is why the
report says "21 of 29 files".

The rule throughout: **when molt is uncertain, it reports instead of rewriting.**
A tool that edits your code has to earn trust in one direction only.

## The traps molt knows about

These are the migrations that look like a rename and are not. They are the whole
reason a careful tool beats a find-and-replace, and each one is pinned by a test.

| Looks like | Actually |
|---|---|
| `x/exp/slices.SortFunc` → `slices.SortFunc` | The comparison changed from `less(a, b) bool` to `cmp(a, b) int`. An import swap **compiles and then sorts wrongly.** |
| `x/exp/maps.Keys` → `maps.Keys` | Return type changed from a slice to an `iter.Seq`. Needs `slices.Collect`. |
| `google/uuid.Nil` → `uuid.Nil` | A package **variable** in google/uuid, a **function** in the standard library. `uuid.Nil` must become `uuid.Nil()`. |
| `google/uuid.NewRandom` → `uuid.NewV4` | google returns `(UUID, error)`; the stdlib returns `UUID` alone. The arity of the call site changes. |
| `pkg/errors.Wrap(err, msg)` | Becomes `fmt.Errorf("%s: %w", msg, err)` — the arguments swap places. Not a rename. |

`slices.SortFunc` is the one worth staring at. Both versions compile. Both run.
One of them sorts your data incorrectly. molt refuses the file.

## Validated against real repositories

molt was run against 12 real Go projects, **5,060 Go files** in total. Nothing in
this table is a fixture.

| Repository | Go files | Result |
|---|---|---|
| ory/kratos | 1,376 | `pkg/errors`: 1,785 uses in 286 files — **40 migratable now** |
| jaegertracing/jaeger | 1,166 | 6 advisory |
| minio/minio | 902 | 1 removable, 2 partly (**21 of 29** uuid files), 4 advisory |
| docker/cli | 724 | 4 advisory (`logrus` 82 uses, `cobra` 1,120 uses) |
| gofiber/fiber | 308 | **`google/uuid` fully removable** |
| go-kit/kit | 248 | 3 advisory |
| prometheus/client_golang | 162 | `json-iterator`: 83 uses in 3 files |
| sirupsen/logrus | 53 | dot-import and shadowing both detected |
| spf13/viper | 33 | shadowing detected in `overrides_test.go` |
| gorilla/mux | 17 | 1 advisory |
| openfaas/faas | 58 | 1 advisory |
| heptiolabs/healthcheck | 13 | 1 advisory |

**Every finding was legitimate on inspection** — no false positives. I did not
attempt an exhaustive false-negative audit of 5,060 files; what I did check was
the one result that looked like a miss, and it was correct. Two results are worth
calling out:

- docker/cli imports `pkg/errors` — but only inside `vendor/`, which molt skips
  exactly as the go command does. It correctly reported nothing.
- The shadowing and dot-import defences **fired on real code**, in logrus and
  viper, not just in tests. The conservative design is not theoretical.

The 23 files molt rewrote in minio were all `gofmt`-clean afterwards.

Performance: 1,376 files scanned in **0.51s**. No network, no module cache read,
no clock.

## Limitations

Stated plainly, because they matter more than the feature list.

1. **Go only.** The idea depends on the standard library shipping a parser, which
   is not true everywhere.
2. **No type checking.** molt matches import declarations against selector
   expressions. It handles the cases where that is insufficient by refusing them,
   rather than by resolving them. A type-aware version would migrate more files;
   it would also be a much larger tool.
3. **Shadowing detection is file-wide, not scope-aware.** If a file binds
   `slices` anywhere, molt treats the whole file as unsafe. This over-reports and
   costs molt some rewrites it could have made safely. The opposite error would
   corrupt code.
4. **Build-tagged files are not excluded.** molt reads every `.go` file
   regardless of build constraints. For the "unused dependency" finding this can
   produce a false alarm, which is why that finding is worded as a prompt to look
   rather than a verdict.
5. **molt never edits `go.mod`.** It tells you to run `go mod tidy`. Rewriting
   the manifest is the go command's job and it does it better.
6. **The corpus is hand-written and finite.** 24 rows. It covers every row of the
   organisers' published Go table plus the ones I found useful, and it will miss
   dependencies it has never heard of. `molt -corpus` prints exactly what it
   knows; the report's KEEP section names what it does not.
7. **Mechanical wins are rarer on well-maintained code.** The honest finding from
   the validation run: modern, actively-maintained repos have mostly already
   migrated off `x/exp/slices` and `x/net/context`. The value is concentrated in
   `google/uuid` (nine days old at time of writing) and in the long tail of older
   codebases.

molt does not claim your tests will pass after `-apply`. It claims the edit is
behaviour-preserving for the symbols it permits, and that you should run your
tests — which is why the command tells you to.

## Zero-dependency proof

```bash
make deps-proof     # regenerates deps-proof.txt
```

The decisive check, which anyone can run:

```bash
go list -deps ./... | grep -v '^molt' | awk -F/ '$1 ~ /\./'
```

`go list -deps` prints every package in the build, transitively. Filtering for a
dot in the first path element finds anything outside the standard library,
because every module path outside it begins with a domain name. **The output is
empty.** See [deps-proof.txt](deps-proof.txt) for the full record.

`go.mod` in full:

```
module molt

go 1.25
```

No require block. No `go.sum`. No `vendor/`. No `golang.org/x/...`.

The only third-party names in this repository are **import paths inside
`testdata/` fixture modules** — text that molt's own tests parse. Those fixtures
are never compiled: `testdata` is skipped by the go command and by molt's
scanner. Their `go.mod` files declare requires on purpose, so molt has something
to find.

## Reproducible build

The bonus challenge, taken as one thing done properly.

```bash
make repro
```

Builds twice, clearing the build cache in between, and compares SHA-256 hashes.
Verified byte-identical on three platforms with Go 1.27.0:

| Target | SHA-256 |
|---|---|
| windows/amd64 | `7e63dbec4905cb49bace779061866a2cc2df14b3bb611ff25954938bb809519f` |
| linux/amd64 | `a2404cdbeff1972aba206b425eb6d89160b0a98fb476f7c9efed93f5835a457e` |
| darwin/arm64 | `ada1bb2e7437628e2d36b83946a50cbc4b4680fd6ebf8555d8918ae0de7eeb90` |

Determinism in Go is not free. Three things break it by default:

1. Absolute source paths are embedded — `-trimpath` removes them.
2. **Since Go 1.24 the toolchain stamps VCS information into the binary**, so the
   commit hash and the dirty flag change the bytes. `-buildvcs=false` turns it
   off. This is the one most people miss.
3. The build id varies — `-ldflags "-buildid="` clears it.

Plus `CGO_ENABLED=0` to keep the host C toolchain out, and a pinned
`GOTOOLCHAIN` so a different Go version cannot silently change the output.

molt also embeds no build timestamp and no commit hash. A version string that
changed every build would be worth less than a reproducible artifact.

## Tests

```bash
make test        # or: go test ./...
```

The suite is behavioural. The ones worth reading:

- **`TestApplyProducesBuildableZeroDepModule`** — applies every mechanical
  migration to a fixture, then runs `go build` and `go test` with **`GOPROXY=off`**.
  A build that still needed a module download would fail rather than quietly
  fetch one. That is what makes it a zero-dependency proof and not just a compile
  check.
- **`TestCheatSheetTableIsCovered`** — pins the corpus to every row of the
  organisers' published Go table, with the release version each landed in.
- **`TestTrapsArePinned`** — asserts that `slices.SortFunc`, `maps.Keys`,
  `uuid.Nil` and friends stay blocked. If someone "helpfully" unblocks one, the
  suite fails.
- **`TestOutputIsDeterministic`** — runs the whole pipeline twice and compares
  bytes, for text, verbose and JSON.
- **`TestRefusesShadowedName`**, **`TestRefusesBlockedSymbol`**,
  **`TestRefusesAliasedSplit`** — the refusals, asserted as features.

Coverage runs 78–95% per package.

## JSON output

`molt -json .` emits the report as a single object. The fields that matter:

```jsonc
{
  "module": "github.com/example/orders",
  "go_files": 4,
  "direct_deps": 9,
  "indirect_deps": 4,
  "sum_modules": 203,
  "removable": 3,            // migrations molt can apply in full
  "partially_removable": 1,  // ... in some files
  "advisory": 5,             // need a human
  "orphans": 2,              // declared, never imported
  "findings": [
    {
      "kind": "migration",           // or "orphan"
      "module": "golang.org/x/exp/slices",
      "target": "slices",
      "since": "go1.21",
      "auto": true,                  // molt can migrate every file
      "partial": false,              // ... or only some
      "auto_files": ["store/store.go"],
      "blocked_files": [],
      "blockers": [],
      "symbols": [
        { "name": "Sort", "count": 2, "first": "store/store.go:18" }
      ],
      "why": "slices entered the standard library in Go 1.21.",
      "guidance": "Swap the import.",
      "note": "The Func variants are the trap."
    }
  ],
  "kept": [ { "module": "github.com/aws/aws-sdk-go-v2", "symbols": 18, "refs": 94 } ],
  "corpus_rows": 24,
  "corpus_mechanical": 6
}
```

Field order and array order are stable, so the output diffs cleanly between runs.

## Repository layout

```
cmd/molt/          the CLI: flags, dispatch, exit codes
internal/gomod/    go.mod and go.sum parsing
internal/scan/     go/ast source analysis: imports, selectors, shadowing
internal/corpus/   the migration table, and what is blocked and why
internal/analyze/  joins declared against used, produces findings
internal/rewrite/  go/ast edits, import regrouping, output verification
internal/diff/     unified diff
internal/render/   text and JSON output
testdata/          fixture modules, never built
scripts/           reproducible build, dependency proof
```

The submission guidance calls for `src/` but notes the layout is advisory and
that judges read what you ship. Go's convention is `cmd/` plus `internal/`, and
`internal/` is enforced by the compiler rather than by agreement, so this reads
as idiomatic Go rather than as a translated layout.

## Corpus source

The migration table is sourced from the standard-library cheat-sheet published at
<https://zerodepshack.com/cheatsheets>, section "Go 1.27 — instead of installing
it", cross-checked against the release notes for each version cited. Every row
carries the Go release that made it possible.

The `google/uuid` row was verified against `go doc uuid` on a Go 1.27.0
toolchain rather than against a summary of it — which is how the `Nil`
variable-versus-function trap was found. Signatures matter more than names when
you are about to edit somebody's code.

See [STDLIB.md](STDLIB.md) for what molt itself replaced.

## License

MIT. See [LICENSE](LICENSE).
