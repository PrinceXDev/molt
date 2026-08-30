# example-app (fixture)

This is a deliberately dependency-heavy fixture used by molt's tests and demo.
It is never built and never part of molt's own module: the directory is named
`testdata`, which both the go command and molt's own scanner skip.

Its `go.mod` has a require block on purpose. molt's `go.mod`, at the repository
root, has none.
