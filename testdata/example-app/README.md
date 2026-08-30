# example-app (fixture)

This is a deliberately dependency-heavy fixture used by molt's tests and demo.
It is never built and never part of molt's own module: the directory is named
`testdata`, which both the go command and molt's own scanner skip.

Its `go.mod` has a require block on purpose. molt's `go.mod`, at the repository
root, has none.

Its `go 1.24` directive is also deliberate. `store/store.go` calls
`uuid.New()` from `github.com/google/uuid`, and the stdlib `uuid` package
needs Go 1.27. This fixture is what molt's go-version gate exists to protect:
`molt -apply` here must leave the uuid import alone, because rewriting it
would leave a module that claims 1.24 support while depending on a
1.27-only standard-library package. See
`cmd/molt.TestApplyLeavesAdvisoryFilesUntouched`.
