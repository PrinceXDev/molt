# tidy-app (fixture)

A fixture whose every dependency is one molt can migrate mechanically. After
`molt -apply`, the module needs no third-party code at all: it compiles and its
tests pass with an empty `go.mod`.

`internal/rewrite`'s end-to-end test proves exactly that, which is why this
fixture exists. It is never built as part of molt: `testdata` is skipped by both
the go command and molt's own scanner.
