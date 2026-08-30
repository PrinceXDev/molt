// Package corpus holds which third-party packages the standard library has
// absorbed, and how.
//
// Rows come from https://zerodepshack.com/cheatsheets, checked against the
// release notes for the version in each Since field. A row is one of two kinds:
//
//   - Mechanical: carries a symbol table, and is rewritten. A file using a
//     symbol absent from that table is refused, not guessed at.
//   - Advisory: guidance only, because the replacement changes the shape of the
//     code rather than its names.
//
// Blocked holds the near-misses: symbols that look like renames and are not.
package corpus

import "sort"

// Repl is the replacement for one symbol.
type Repl struct {
	// Target overrides the migration's Target for this symbol. pkg/errors needs
	// it: New belongs to errors, Errorf belongs to fmt.
	Target string
	// Symbol is the new name, empty when it is unchanged.
	Symbol string
}

// Migration describes one third-party package the standard library replaced.
type Migration struct {
	// Module is the import path as it appears in source.
	Module string
	// Pkg is the identifier the package declares, which is not always the last
	// element of the path.
	Pkg string

	// Target is the stdlib import path, and TargetPkg the identifier it
	// declares.
	Target    string
	TargetPkg string

	// Since names the Go release that made the migration possible.
	Since string

	// Symbols maps a symbol to its replacement. A nil map on a mechanical row
	// means every symbol survives unchanged and only the import path moves.
	Symbols map[string]Repl

	// Blocked names symbols that look migratable but are not, with the reason.
	// Their presence in a file makes the whole file advisory.
	Blocked map[string]string

	// Advisory marks a row molt will never rewrite.
	Advisory bool

	// Verified records whether the replacement API was checked against the
	// released standard library rather than a summary of it. An unverified row
	// is treated as advisory regardless of its symbol table.
	Verified bool

	// Why is one line on what the standard library gives you.
	Why string
	// Guidance is what a human must do, for advisory rows.
	Guidance string
	// Note records anything a reader should know before trusting the row.
	Note string
}

// Mechanical reports whether molt may rewrite this migration at all.
func (m Migration) Mechanical() bool { return !m.Advisory && m.Verified }

// Replacement resolves one symbol. ok is false when the symbol is not covered,
// which blocks the rewrite for the file that uses it.
func (m Migration) Replacement(symbol string) (target, name string, ok bool) {
	if _, blocked := m.Blocked[symbol]; blocked {
		return "", "", false
	}
	if m.Symbols == nil {
		return m.Target, symbol, true
	}
	r, found := m.Symbols[symbol]
	if !found {
		return "", "", false
	}
	target = r.Target
	if target == "" {
		target = m.Target
	}
	name = r.Symbol
	if name == "" {
		name = symbol
	}
	return target, name, true
}

// BlockReason explains why a symbol cannot be rewritten, for the report.
func (m Migration) BlockReason(symbol string) string {
	if reason, ok := m.Blocked[symbol]; ok {
		return reason
	}
	if m.Symbols != nil {
		if _, found := m.Symbols[symbol]; !found {
			return "not in molt's verified replacement table"
		}
	}
	return ""
}

// all is the corpus. Keep it sorted by module path; the tests enforce it.
var all = []Migration{
	{
		Module:    "github.com/dustin/go-humanize",
		Pkg:       "humanize",
		Target:    "strconv, fmt and time",
		TargetPkg: "",
		Since:     "go1.0",
		Advisory:  true,
		Why:       "Byte, comma and relative-time formatting is arithmetic plus strconv.",
		Guidance:  "Each helper is a handful of lines: humanize.Bytes is a divide-and-label loop, humanize.Comma is strconv plus insertion, humanize.Time is a time.Since ladder. Port only the ones you call.",
	},
	{
		Module:    "github.com/fatih/color",
		Pkg:       "color",
		Target:    "raw ANSI escapes",
		TargetPkg: "",
		Since:     "go1.0",
		Advisory:  true,
		Why:       "Go has no terminal-colour package because colour is a two-character escape sequence.",
		Guidance:  "Emit \\x1b[31m and \\x1b[0m directly. Gate on a TTY check and honour NO_COLOR; that gate is the part a colour library actually earns its keep on.",
	},
	{
		Module:    "github.com/go-chi/chi",
		Pkg:       "chi",
		Target:    "net/http",
		TargetPkg: "http",
		Since:     "go1.22",
		Advisory:  true,
		Why:       "net/http.ServeMux gained method and wildcard patterns in 1.22.",
		Guidance:  "As for chi/v5: register routes as \"GET /items/{id}\" on http.NewServeMux and read parameters with r.PathValue.",
		Note:      "This is the pre-module-suffix import path, still present in older code.",
	},
	{
		Module:    "github.com/go-chi/chi/v5",
		Pkg:       "chi",
		Target:    "net/http",
		TargetPkg: "http",
		Since:     "go1.22",
		Advisory:  true,
		Why:       "net/http.ServeMux gained method and wildcard patterns in 1.22.",
		Guidance:  "Register routes as \"GET /items/{id}\" on http.NewServeMux and read parameters with r.PathValue(\"id\"). Middleware becomes ordinary http.Handler wrapping. Sub-routers become a second ServeMux mounted with Handle and a trailing wildcard.",
		Note:      "Chi's middleware ecosystem has no stdlib equivalent. If you use more than a handful, keeping chi is the honest call.",
	},
	{
		Module:    "github.com/gocarina/gocsv",
		Pkg:       "gocsv",
		Target:    "encoding/csv",
		TargetPkg: "csv",
		Since:     "go1.0",
		Advisory:  true,
		Why:       "encoding/csv reads and writes CSV; gocsv adds struct tag binding on top.",
		Guidance:  "Replace the struct-tag marshalling with an explicit header-index map and field assignment. More lines, but the mapping stops being invisible.",
	},
	{
		Module:    "github.com/google/uuid",
		Pkg:       "uuid",
		Target:    "uuid",
		TargetPkg: "uuid",
		Since:     "go1.27",
		Verified:  true,
		// Verified against go doc uuid on go1.27.0. Only these four have
		// signatures identical to google/uuid's.
		Symbols: map[string]Repl{
			"New":       {}, // func() UUID in both
			"Parse":     {}, // func(string) (UUID, error) in both
			"MustParse": {}, // func(string) UUID in both
			"UUID":      {}, // [16]byte in both
		},
		Blocked: map[string]string{
			"Nil":              "a package variable in google/uuid, a function in the stdlib, so uuid.Nil must become uuid.Nil()",
			"Max":              "a package variable in google/uuid, a function in the stdlib, so uuid.Max must become uuid.Max()",
			"NewString":        "no stdlib equivalent; the closest is uuid.New().String()",
			"NewRandom":        "google/uuid returns (UUID, error); stdlib NewV4 returns UUID alone, so the error result disappears",
			"NewV7":            "google/uuid returns (UUID, error); stdlib NewV7 returns UUID alone",
			"NewV6":            "the stdlib provides v4 and v7 only",
			"NewUUID":          "version 1 UUIDs are not in the stdlib",
			"NewMD5":           "name-based version 3 UUIDs are not in the stdlib",
			"NewSHA1":          "name-based version 5 UUIDs are not in the stdlib",
			"NewDCEGroup":      "DCE UUIDs are not in the stdlib",
			"NewDCEPerson":     "DCE UUIDs are not in the stdlib",
			"NewDCESecurity":   "DCE UUIDs are not in the stdlib",
			"Must":             "no stdlib equivalent; the stdlib constructors do not return errors to absorb",
			"FromBytes":        "no stdlib equivalent; convert a [16]byte to uuid.UUID directly",
			"ParseBytes":       "no stdlib equivalent; use Parse(string(b))",
			"Validate":         "no stdlib equivalent; call Parse and discard the value",
			"SetRand":          "the stdlib does not expose its random source",
			"SetClockSequence": "no stdlib equivalent",
			"EnableRandPool":   "no stdlib equivalent; the stdlib manages its own randomness",
			"DisableRandPool":  "no stdlib equivalent",
			"NameSpaceDNS":     "namespace values exist for v3 and v5, which the stdlib does not provide",
			"NameSpaceURL":     "namespace values exist for v3 and v5, which the stdlib does not provide",
			"NameSpaceOID":     "namespace values exist for v3 and v5, which the stdlib does not provide",
			"NameSpaceX500":    "namespace values exist for v3 and v5, which the stdlib does not provide",
		},
		Why:      "RFC 9562 UUIDs moved into the standard library in Go 1.27.",
		Guidance: "New, Parse, MustParse and the UUID type are drop-in: change the import path and nothing else. Anything generating a version other than 4, or touching uuid.Nil, needs hands.",
		Note:     "Both New functions return a UUID and neither reports an error, so the swap is behaviour-preserving. Nil and Max are the trap: google/uuid exports them as variables and the stdlib as functions, so a blind import swap fails to compile. molt refuses any file that touches them.",
	},
	{
		Module:    "github.com/gorilla/csrf",
		Pkg:       "csrf",
		Target:    "net/http",
		TargetPkg: "http",
		Since:     "go1.25",
		Advisory:  true,
		Why:       "net/http.CrossOriginProtection is stdlib anti-CSRF, added in Go 1.25.",
		Guidance:  "Construct http.NewCrossOriginProtection, register trusted origins, and wrap your handler. The token-in-form model disappears; the check is Sec-Fetch-Site and Origin based.",
		Note:      "This is a different defence, not a drop-in. If you must support clients that send neither header, read the docs before switching.",
	},
	{
		Module:    "github.com/gorilla/mux",
		Pkg:       "mux",
		Target:    "net/http",
		TargetPkg: "http",
		Since:     "go1.22",
		Advisory:  true,
		Why:       "net/http.ServeMux gained method and wildcard patterns in 1.22.",
		Guidance:  "mux.NewRouter becomes http.NewServeMux. r.HandleFunc(\"/items/{id}\", h).Methods(\"GET\") becomes mux.HandleFunc(\"GET /items/{id}\", h). mux.Vars(r)[\"id\"] becomes r.PathValue(\"id\").",
		Note:      "Regexp route constraints and gorilla's Subrouter matching have no stdlib equivalent. Routes using them need rethinking, not translating.",
	},
	{
		Module:    "github.com/joho/godotenv",
		Pkg:       "godotenv",
		Target:    "os",
		TargetPkg: "os",
		Since:     "go1.0",
		Advisory:  true,
		Why:       "Reading KEY=value lines into the environment is a ten-line function.",
		Guidance:  "bufio.Scanner over the file, strings.Cut on the first equals sign, os.Setenv. The subtleties worth keeping are quoted values and the export prefix; decide whether you actually use them.",
	},
	{
		Module:    "github.com/json-iterator/go",
		Pkg:       "jsoniter",
		Target:    "encoding/json/v2",
		TargetPkg: "json",
		Since:     "go1.27",
		Advisory:  true,
		Why:       "encoding/json/v2 graduated from GOEXPERIMENT in Go 1.27; the performance gap that justified json-iterator is largely gone.",
		Guidance:  "Replace jsoniter.Marshal and Unmarshal with the v2 equivalents. jsoniter's ConfigCompatibleWithStandardLibrary indirection disappears entirely.",
		Note:      "Benchmark before and after on your own payloads. Claiming a speedup you did not measure is worse than keeping the dependency.",
	},
	{
		Module:    "github.com/mitchellh/go-homedir",
		Pkg:       "homedir",
		Target:    "os",
		TargetPkg: "os",
		Since:     "go1.12",
		Verified:  true,
		Symbols: map[string]Repl{
			"Dir": {Symbol: "UserHomeDir"},
		},
		Blocked: map[string]string{
			"Expand":       "os has no tilde expansion; write it with strings.HasPrefix and filepath.Join",
			"Reset":        "no stdlib equivalent; go-homedir caches and os.UserHomeDir does not",
			"DisableCache": "no stdlib equivalent; os.UserHomeDir never caches",
		},
		Why:      "os.UserHomeDir has the same (string, error) signature and has been in the standard library since Go 1.12.",
		Guidance: "homedir.Dir() becomes os.UserHomeDir().",
	},
	{
		Module:    "github.com/patrickmn/go-cache",
		Pkg:       "cache",
		Target:    "sync",
		TargetPkg: "sync",
		Since:     "go1.0",
		Advisory:  true,
		Why:       "An in-process TTL cache is a map, a mutex and an expiry timestamp.",
		Guidance:  "A struct holding sync.RWMutex and map[string]entry, where entry carries a deadline, covers the common case. Eviction on read is simpler than a sweeper goroutine and usually enough.",
	},
	{
		Module:    "github.com/pkg/errors",
		Pkg:       "errors",
		Target:    "errors",
		TargetPkg: "errors",
		Since:     "go1.13",
		Verified:  true,
		Symbols: map[string]Repl{
			"New":    {Target: "errors", Symbol: "New"},
			"Is":     {Target: "errors", Symbol: "Is"},
			"As":     {Target: "errors", Symbol: "As"},
			"Unwrap": {Target: "errors", Symbol: "Unwrap"},
			"Errorf": {Target: "fmt", Symbol: "Errorf"},
		},
		Blocked: map[string]string{
			"Wrap":        "argument shape changes: Wrap(err, msg) becomes fmt.Errorf(\"%s: %w\", msg, err)",
			"Wrapf":       "argument shape changes: the wrapped error moves to the end of the format arguments",
			"WithMessage": "argument shape changes, as with Wrap",
			"WithStack":   "no stdlib equivalent; the standard library does not capture stack traces in errors",
			"Cause":       "errors.Unwrap goes one level; Cause unwraps to the root. Loop, or use errors.Is against the target",
		},
		Why:      "Error wrapping with %w landed in Go 1.13, and errors.Is, As and Unwrap came with it.",
		Guidance: "New, Errorf, Is, As and Unwrap are drop-in. Wrap and Wrapf are the ones that need hands: fmt.Errorf(\"%s: %w\", msg, err).",
		Note:     "WithStack has no replacement. A project relying on stack traces in errors is not a candidate for this migration.",
	},
	{
		Module:    "github.com/rs/zerolog",
		Pkg:       "zerolog",
		Target:    "log/slog",
		TargetPkg: "slog",
		Since:     "go1.21",
		Advisory:  true,
		Why:       "log/slog is structured, levelled logging in the standard library.",
		Guidance:  "slog.New(slog.NewJSONHandler(w, nil)) gives you zerolog's JSON output. The chained Str/Int builder becomes variadic slog.Attr arguments.",
		Note:      "zerolog's zero-allocation claim is real and slog does allocate more. If your hot path logs heavily, measure before switching.",
	},
	{
		Module:    "github.com/sirupsen/logrus",
		Pkg:       "logrus",
		Target:    "log/slog",
		TargetPkg: "slog",
		Since:     "go1.21",
		Advisory:  true,
		Why:       "log/slog is structured, levelled logging in the standard library, added in Go 1.21.",
		Guidance:  "logrus.Info(\"msg\") becomes slog.Info(\"msg\"). logrus.WithFields(logrus.Fields{\"k\": v}).Info(\"msg\") becomes slog.Info(\"msg\", \"k\", v). A logrus.Entry becomes the *slog.Logger returned by With.",
		Note:      "logrus Hooks have no direct equivalent; the closest shape is a custom slog.Handler that wraps another.",
	},
	{
		Module:    "github.com/spf13/cobra",
		Pkg:       "cobra",
		Target:    "flag",
		TargetPkg: "flag",
		Since:     "go1.0",
		Advisory:  true,
		Why:       "flag plus a switch over the first argument covers a single-level subcommand CLI.",
		Guidance:  "One flag.NewFlagSet per subcommand, a switch on os.Args[1], and a hand-written usage function. You lose completion scripts and nested command trees.",
		Note:      "For a CLI with deep nesting, generated completions or a plugin system, cobra earns its place. This row is for the common two-command case.",
	},
	{
		Module:    "github.com/stretchr/testify/assert",
		Pkg:       "assert",
		Target:    "testing + testing/synctest",
		TargetPkg: "testing",
		Since:     "go1.25",
		Advisory:  true,
		Why:       "testing could always assert; testing/synctest in Go 1.25 closed the last gap by making concurrency tests deterministic.",
		Guidance:  "assert.Equal(t, want, got) becomes an if statement with t.Errorf. For structs, reflect.DeepEqual does the comparison. Keep the failure message: that is the part testify was really providing.",
		Note:      "This is a test-only dependency. It never ships in the runtime artifact, so removing it is about craft rather than supply-chain surface.",
	},
	{
		Module:    "github.com/stretchr/testify/require",
		Pkg:       "require",
		Target:    "testing + testing/synctest",
		TargetPkg: "testing",
		Since:     "go1.25",
		Advisory:  true,
		Why:       "require is assert that stops the test, which is t.Fatalf.",
		Guidance:  "require.NoError(t, err) becomes if err != nil { t.Fatalf(...) }. The mechanical difference from assert is Fatalf rather than Errorf.",
		Note:      "Test-only, as with assert.",
	},
	{
		Module:    "go.uber.org/zap",
		Pkg:       "zap",
		Target:    "log/slog",
		TargetPkg: "slog",
		Since:     "go1.21",
		Advisory:  true,
		Why:       "log/slog covers structured levelled logging without a dependency.",
		Guidance:  "zap.L().Info(\"msg\", zap.String(\"k\", v)) becomes slog.Info(\"msg\", \"k\", v), or slog.String for the typed form. SugaredLogger maps onto slog directly.",
		Note:      "zap is faster than slog under load. Switching for craft is fine; switching a high-throughput logging path without measuring is not.",
	},
	{
		Module:    "golang.org/x/exp/constraints",
		Pkg:       "constraints",
		Target:    "cmp",
		TargetPkg: "cmp",
		Since:     "go1.21",
		Advisory:  true,
		Why:       "cmp.Ordered replaced constraints.Ordered when generics settled.",
		Guidance:  "constraints.Ordered becomes cmp.Ordered. Integer, Float and Complex have no stdlib equivalent; declare the union inline where you need it.",
	},
	{
		Module:    "golang.org/x/exp/maps",
		Pkg:       "maps",
		Target:    "maps",
		TargetPkg: "maps",
		Since:     "go1.21",
		Verified:  true,
		Symbols: map[string]Repl{
			"Clone":      {},
			"Copy":       {},
			"DeleteFunc": {},
			"Equal":      {},
			"EqualFunc":  {},
		},
		Blocked: map[string]string{
			"Keys":   "x/exp returns a slice; stdlib maps.Keys returns an iterator. Wrap it: slices.Collect(maps.Keys(m))",
			"Values": "x/exp returns a slice; stdlib maps.Values returns an iterator. Wrap it: slices.Collect(maps.Values(m))",
			"Clear":  "removed; the built-in clear() covers it",
		},
		Why:      "maps entered the standard library in Go 1.21.",
		Note:     "Keys and Values are the trap. They kept their names and changed their return type from a slice to an iter.Seq, so a blind import swap compiles in some call sites and breaks in others. molt refuses any file that uses them.",
		Guidance: "Swap the import. For Keys and Values, wrap the iterator with slices.Collect, or range over it directly.",
	},
	{
		Module:    "golang.org/x/exp/slices",
		Pkg:       "slices",
		Target:    "slices",
		TargetPkg: "slices",
		Since:     "go1.21",
		Verified:  true,
		Symbols: map[string]Repl{
			"Clip":         {},
			"Clone":        {},
			"Compact":      {},
			"CompactFunc":  {},
			"Contains":     {},
			"ContainsFunc": {},
			"Delete":       {},
			"Equal":        {},
			"EqualFunc":    {},
			"Grow":         {},
			"Index":        {},
			"IndexFunc":    {},
			"Insert":       {},
			"IsSorted":     {},
			"Max":          {},
			"Min":          {},
			"Replace":      {},
			"Reverse":      {},
			"Sort":         {},
			"SortStable":   {},
		},
		Blocked: map[string]string{
			"SortFunc":         "the comparison changed from less(a, b) bool to cmp(a, b) int",
			"SortStableFunc":   "the comparison changed from less(a, b) bool to cmp(a, b) int",
			"IsSortedFunc":     "the comparison changed from less(a, b) bool to cmp(a, b) int",
			"BinarySearchFunc": "the comparison changed from less(a, b) bool to cmp(a, b) int",
			"MinFunc":          "the comparison changed from less(a, b) bool to cmp(a, b) int",
			"MaxFunc":          "the comparison changed from less(a, b) bool to cmp(a, b) int",
			"CompareFunc":      "the comparison changed from less(a, b) bool to cmp(a, b) int",
		},
		Why:      "slices entered the standard library in Go 1.21.",
		Note:     "The Func variants are the trap. They kept their names and changed their comparison from a bool less to an int cmp, so an import swap compiles and then sorts wrongly. molt refuses any file that uses them.",
		Guidance: "Swap the import. For the Func variants, convert each comparison to return a negative, zero or positive int, which cmp.Compare already does for ordered types.",
	},
	{
		Module:    "golang.org/x/net/context",
		Pkg:       "context",
		Target:    "context",
		TargetPkg: "context",
		Since:     "go1.7",
		Verified:  true,
		Why:       "context moved into the standard library in Go 1.7. x/net/context has been a type alias shim ever since.",
		Guidance:  "Change the import path. Nothing else moves.",
	},
	{
		Module:    "golang.org/x/sync/errgroup",
		Pkg:       "errgroup",
		Target:    "sync",
		TargetPkg: "sync",
		Since:     "go1.25",
		Advisory:  true,
		Why:       "sync.WaitGroup.Go arrived in Go 1.25 and removes the boilerplate errgroup existed to hide.",
		Guidance:  "For the wait-for-all case, wg.Go(func() { ... }) plus a mutex-guarded first error is a direct swap. errgroup.WithContext, which cancels siblings on the first failure, needs a context.WithCancelCause by hand.",
		Note:      "SetLimit has no one-line equivalent; a buffered channel used as a semaphore is the usual shape.",
	},
}

// All returns the corpus in module order.
func All() []Migration {
	out := make([]Migration, len(all))
	copy(out, all)
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

// Lookup finds the migration for an exact import path.
func Lookup(importPath string) (Migration, bool) {
	for _, m := range all {
		if m.Module == importPath {
			return m, true
		}
	}
	return Migration{}, false
}

// Count reports the corpus size, split by kind, for the report footer.
func Count() (total, mechanical int) {
	for _, m := range all {
		total++
		if m.Mechanical() {
			mechanical++
		}
	}
	return total, mechanical
}
