# STDLIB.md

Every third-party package molt would normally have needed, and what replaced it.

Fourteen substitutions. Each entry says what got harder and what tradeoff was
accepted, because the ones with no downside are the boring ones.

**Disclosure up front:** molt contains no vendored third-party source, and no
dev-only test dependency. Go ships `testing`, so the test-framework exemption in
the rules was not needed. Every line in `cmd/` and `internal/` was written for
this submission.

---

## 1. golang.org/x/tools/go/packages → go/parser + go/ast

**This is the one that decides whether molt can exist.**

Every Go static-analysis tool loads source through `go/packages`. It is the
canonical answer, it is excellent, and it is not standard library — and the event
rules say explicitly that `golang.org/x` is not a free pass.

molt's core question is: *which exported names of package P does this file
reference?* I initially assumed that needed type resolution, which would have
meant `go/packages` and the end of the project.

It does not. Import declarations and selector expressions are both **syntax**:

```go
af, _ := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
for _, spec := range af.Imports { /* local name -> import path */ }
ast.Inspect(af, func(n ast.Node) bool {
    if sel, ok := n.(*ast.SelectorExpr); ok {
        if id, ok := sel.X.(*ast.Ident); ok {
            // id.Name qualifies sel.Sel.Name
        }
    }
    return true
})
```

**What was harder:** without types, `uuid.New` and `myvar.New` are the same shape.
Type information is genuinely needed to tell them apart when a local binding
shadows a package name.

**Tradeoff accepted:** molt does not resolve those cases, it *detects and refuses*
them. `scan.DeclaredNames` collects every identifier a file binds — locals,
parameters, receivers, types, range variables, struct fields — and any import
whose name collides is marked unsafe for that file. The detection is file-wide
rather than scope-aware, which over-reports and costs molt some rewrites it could
have made safely.

**Why that is the right call here:** the failure modes are asymmetric. Refusing a
safe rewrite wastes an opportunity; performing an unsafe one corrupts code. And
the refusal is not hypothetical — it fired on real repositories during
validation, in `sirupsen/logrus` (dot import) and `spf13/viper` (shadowed name).

`go/packages` would give molt more reach. It would also make it a fundamentally
larger program, and it would not have taught me that the parser was enough.

---

## 2. golang.org/x/tools/go/ast/astutil → hand-rolled import editing

`astutil.AddImport` and `astutil.DeleteImport` are how everyone adds and removes
imports. Same problem: `golang.org/x`.

molt manipulates `*ast.GenDecl` directly — mutating an `ImportSpec`'s path,
appending specs, removing them, and deleting the declaration when it empties.

**What was harder:** two things, and both cost real time.

First, `go/printer` only emits parentheses when `GenDecl.Lparen` holds a *valid
position*. A single-line `import "x"` that gains a second spec silently prints as
one broken line unless you promote it:

```go
if !gen.Lparen.IsValid() {
    gen.Lparen = gen.TokPos + token.Pos(len("import"))
    gen.Rparen = gen.Lparen
}
```

Second — **the bug that ate the most time** — replacing an identifier with
`ast.NewIdent` produced this:

```go
slices.
    Sort(s)
```

`ast.NewIdent` carries `token.NoPos`. `go/printer` reads the gap between a node's
position and the next one as a line break, so a zero-position qualifier followed
by a real-position selector becomes two lines. The fix is to mutate the existing
identifier's `Name` field rather than replace the node, so the position survives:

```go
c.sel.X.(*ast.Ident).Name = pkg   // not: c.sel.X = ast.NewIdent(pkg)
```

That single distinction is the difference between output a reviewer accepts and
output that looks machine-mangled.

**Tradeoff accepted:** molt only handles the import shapes it actually needs —
one path in, one or two paths out. It is not a general-purpose import editor.

---

## 3. golang.org/x/mod/modfile → a hand-written go.mod parser

`modfile` is the official parser and it handles the entire grammar.

molt's `internal/gomod` parses the subset it needs: `module`, `go`, `toolchain`,
and `require` in both the single-line and parenthesised forms, including the
`// indirect` marker.

**What was harder:** the grammar has more corners than it looks. Paths can be
quoted. `// indirect` can be followed by other words. Block directives need
skipping without swallowing the rest of the file. Real-world `go.mod` files
contain `replace`, `exclude`, `retract`, `tool` and `godebug`.

**Tradeoff accepted:** unknown directives are counted rather than parsed, so molt
can *report* that a `replace` exists without resolving it. A malformed require
line is skipped rather than fatal — a partial dependency list is more useful than
no answer. Only a missing `module` directive is fatal, which is also what the go
command does.

**Why it was appropriate:** molt reads four fields. Pulling in a full grammar
implementation for that is exactly the reflex this hackathon is about. About 200
lines, and `TestParseTolerance` pins the ugly cases.

---

## 4. github.com/sergi/go-diff → an LCS line differ

`molt -diff` needs a unified diff, and the standard library has none. The usual
answers are `sergi/go-diff` or `google/go-cmp`.

`internal/diff` is a longest-common-subsequence table over lines.

**What was harder:** an LCS table is O(n×m) in memory. A 5,000-line file against
itself would be 25 million cells.

**The fix that made it viable:** trim the common prefix and suffix *before*
building the table. molt's edits touch an import block and a few call sites, so
the differing middle is a few dozen lines even in a huge file. `TestChangeInsideLargeFile`
changes one line in the middle of a 5,000-line file and asserts the diff comes
out under 20 lines.

**Tradeoff accepted, stated honestly:** this is not Myers' algorithm and it is
slower than `go-diff` on genuinely dissimilar inputs. Above 2,000 differing lines
molt degrades to a whole-region replacement hunk rather than allocating without
bound. For molt's actual inputs the difference is unmeasurable; for a general
diff library it would not be acceptable.

---

## 5. goimports → hand-rolled import grouping

After a rewrite, `go/printer` packs every spec into one group:

```go
import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"slices"
)
```

That is `gofmt`-valid and it is wrong to any Go reviewer. `goimports` is a
separate binary, not a library molt can call, and shelling out to a tool
installed separately is explicitly out of scope.

molt regenerates the import block textually, partitioned into standard library
and everything else. The stdlib test is the one `goimports` itself uses: **a
standard-library path has no dot in its first element**, because every module
path outside it begins with a domain name.

**What was harder:** forcing a blank line between two specs through `go/printer`
means fabricating token positions, which is far more fragile than splicing bytes.
So molt prints the file, locates the declaration's byte range from the re-parsed
AST, and swaps in a regenerated block, then runs `format.Source` over the result.

**Tradeoff accepted:** regeneration would drop comments inside the import block,
so molt **declines to regroup** when the block contains any. It leaves the
packed-but-valid form instead. Losing a reader's comment to cosmetics is not a
trade worth making.

---

## 6. github.com/spf13/cobra → flag

cobra gives subcommand trees, generated shell completions and help templates.

molt takes one path and a handful of booleans. `flag.NewFlagSet` plus a
positional argument is the whole surface, with a hand-written `Usage` that lists
the exit codes and five worked examples.

**Tradeoff accepted:** no shell completion, and no room to grow a subcommand tree
without restructuring. `-corpus` is a flag where `molt corpus` would read better.

**Why it was appropriate:** molt does one thing. A CLI framework for one command
is the definition of a dependency that has not earned its place.

---

## 7. github.com/fatih/color → raw ANSI escapes

molt uses exactly five escape sequences:

```go
const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiGreen = "\x1b[32m"
	ansiAmber = "\x1b[33m"
)
```

**Tradeoff accepted:** no 256-colour or true-colour support, and no Windows
console API fallback for terminals predating VT processing. Modern Windows
terminals handle ANSI; very old `cmd.exe` will show escape characters.

**Why it was appropriate:** colour is a two-character escape. A colour library's
real value is the *decision* of whether to emit it — see the next entry — not the
constants.

---

## 8. github.com/mattn/go-isatty → os.File.Stat + os.ModeCharDevice

The genuinely useful part of a colour library is knowing when to shut up. That is
two standard-library calls:

```go
info, err := f.Stat()
return info.Mode()&os.ModeCharDevice != 0
```

molt disables colour unless the writer is an `*os.File`, that file is a character
device, and `NO_COLOR` is unset. `TestPlainStyleEmitsNoEscapes` and
`TestStyleForNonTerminal` pin it, because escape codes in piped output are the
kind of bug that makes a tool unusable in a pipeline.

**Tradeoff accepted:** none worth reporting. `go-isatty` exists mostly because
this check is obscure, not because it is hard.

---

## 9. github.com/olekukonko/tablewriter → text/tabwriter

The aligned counts block and the KEEP listing are `text/tabwriter` with
tab-separated writes.

**Tradeoff accepted:** no borders, no cell wrapping, no column-width control.
`tabwriter` also counts bytes rather than display width, so wide or combining
characters would misalign — none appear in molt's output, which is import paths
and integers.

**Why it was appropriate:** the standard library already had this one. It is a
substitution people make out of habit rather than need.

---

## 10. github.com/stretchr/testify → testing

Around 1,400 lines of tests, no assertion library.

```go
if got := u.SymbolNames(); len(got) != 1 || got[0] != "New" {
    t.Fatalf("SymbolNames = %v, want [New] resolved through the alias", got)
}
```

**What was harder:** more typing, genuinely. `assert.Equal(t, want, got)` is
shorter than the `if` and the `t.Errorf`.

**What got better, unexpectedly:** writing the message by hand forces you to say
what the test is actually asserting. `testify`'s default output tells you two
values differed; the line above says which behaviour broke. Several test names in
this suite — `TestRefusesUUIDNilVarToFunc`, `TestPromotesSingleLineImport` — came
out of having to articulate the failure.

**Tradeoff accepted:** no `assert.ElementsMatch` for order-insensitive
comparison, and no mocking framework. Neither was needed: molt's core is pure
functions over bytes, which is the shape that does not need mocks.

---

## 11. github.com/dustin/go-humanize → four lines

`humanize` would have been installed for one thing: `"1 file"` versus
`"3 files"`.

```go
func count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
```

This one is on the list because molt *found `go-humanize` in minio* — 275 uses
across 53 files — and it would have been absurd to report that while importing it.

**Tradeoff accepted:** English only, and no byte-size or relative-time
formatting. molt needs neither.

---

## 12. github.com/sirupsen/logrus and log/slog → neither

molt writes diagnostics to `stderr` with `fmt.Fprintf` and returns an exit code.
There is no logger at all, structured or otherwise.

**Why:** a CLI that runs for half a second and prints a report does not have
observability requirements. Reaching for `log/slog` here would be cargo-culting
the shape of a server. The stdlib answer to "do I need a logging framework" is
sometimes "you do not need logging".

**Tradeoff accepted:** no log levels and no structured diagnostics. If molt grew
a daemon mode this would change, and `log/slog` — standard library since Go 1.21 —
would be the answer then.

---

## 13. github.com/spf13/viper → no configuration at all

viper would be the reflex for a config file, environment overrides and flag
binding.

molt has no configuration file. Every input is a flag or the positional path, and
the only environment variable it reads is `NO_COLOR`.

**Why:** a config file for a tool with seven flags adds a second place for
behaviour to hide. The report has to be reproducible from the command line alone
for the determinism guarantee to mean anything — a config file molt silently
picked up from the working directory would quietly break that.

**Tradeoff accepted:** no per-project corpus extensions. A user who wants to add
their own migration has to edit `internal/corpus/corpus.go` and rebuild. For a
72-hour tool that is the right side of the line; a plugin system would be the
wrong one.

---

## 14. github.com/Masterminds/semver → not needed, by design

Worth recording as a **near miss**, because the instinct was there.

molt reports Go versions (`stdlib since go1.21`) and reads module versions from
`go.mod`. My first instinct was that comparing them needed a semver library.

It does not, because molt never compares versions. The `Since` field is a
human-facing string. The decision molt actually makes — *is this symbol
replaceable* — depends on the corpus, not on version arithmetic. A dependency
avoided by noticing the feature was not required.

**Why it belongs here:** the most valuable substitution is the one where you
realise you did not need the capability at all. That is a different move from
reimplementing it, and it happens more often than people expect once you have to
justify every import.

---

## Package Killer

The nomination is **`golang.org/x/tools/go/packages`**, replaced by `go/parser`
and `go/ast` (entry 1), with `golang.org/x/tools/go/ast/astutil` as the companion
kill (entry 2).

The case for it:

- **Real install weight.** `golang.org/x/tools` is a dependency of essentially
  every linter, code generator, language server and refactoring tool in the Go
  ecosystem. If you have written Go tooling, it is in your `go.mod`.
- **The rules name it.** The event's own Go definition says the toolchain and
  `golang.org/x` are *not a free pass*. A static-analysis tool with no
  `golang.org/x` is the sharpest possible reading of that line.
- **It is not a toy replacement.** molt scanned 5,060 real Go files across 12
  production repositories, found the same imports and symbols a type-aware tool
  would, and detected dot-imports and shadowed package names in real code — while
  refusing to act on them rather than guessing.
- **The tradeoff is documented rather than hidden.** No type checking, refusal
  over resolution, file-wide shadowing detection. Entry 1 says exactly what that
  costs.

---

## What the standard library did not have

Recorded so the wins above are not the whole story.

- **No diff.** Entry 4 is a real reimplementation with a real performance
  tradeoff, not a substitution.
- **No import grouping.** `gofmt` does not sort or group imports; `goimports` is
  a separate binary. Entry 5 exists because there was no stdlib answer.
- **No TOML.** `.zero-dep.toml` is a submission artifact that molt itself never
  reads, so this never became a problem. Had molt needed to read it, writing a
  TOML subset parser would have been the project's largest single piece of work —
  the cheat-sheet is right that Go has no answer here.
- **No scope-aware identifier resolution without `go/types`.** The honest gap
  behind entry 1's refusals.
