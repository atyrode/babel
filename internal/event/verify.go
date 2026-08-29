package event

import "strings"

// verificationPrograms is the whole vocabulary that promotes a shell command
// from a tool observation to verification evidence. It is deliberately small
// and literal: the value of KindVerificationEvidence is that a downstream
// finding can say "the agent ran the tests and they failed", so a command
// Babel cannot recognize stays a tool observation rather than being promoted
// to evidence it cannot defend.
//
// A nil subcommand list means the program itself is a check runner. A
// non-empty list means only those first arguments qualify, because `go test`
// is verification while `go run` is not.
var verificationPrograms = map[string][]string{
	// Go
	"go":            {"test", "build", "vet"},
	"gofmt":         nil,
	"golangci-lint": nil,
	"staticcheck":   nil,
	// Rust
	"cargo": {"test", "build", "check", "clippy", "fmt"},
	// JavaScript and TypeScript
	"npm":    {"test", "run"},
	"pnpm":   {"test", "run"},
	"yarn":   {"test", "run"},
	"bun":    {"test"},
	"jest":   nil,
	"vitest": nil,
	"mocha":  nil,
	"tsc":    nil,
	"eslint": nil,
	// Python
	"pytest": nil,
	"tox":    nil,
	"ruff":   nil,
	"mypy":   nil,
	// Other ecosystems
	"ctest":   nil,
	"rspec":   nil,
	"phpunit": nil,
	"dotnet":  {"test", "build"},
	"mvn":     {"test", "verify"},
	"gradle":  {"test", "check", "build"},
	"bazel":   {"test", "build"},
	"cmake":   {"--build"},
	// Project entry points
	"make": {"test", "check", "lint", "build", "verify"},
	"nix":  {"build", "flake"},
	// Shell checkers
	"shellcheck": nil,
}

// commandWrappers are programs that do not verify anything themselves but
// run a nested command line, which agents habitually use: a repository whose
// toolchain lives in a dev shell records `nix develop -c go test ./...`, and
// missing that would leave every one of its test runs unclassified. The
// value lists the flags after which the nested command begins; nil means it
// begins at the first argument that is neither a flag nor an assignment.
//
// The table stays short on purpose. A wrapper that hides the real program
// behind arguments Babel cannot interpret (a container image, a timeout
// duration) is left out, so its command stays a tool observation.
var commandWrappers = map[string][]string{
	"bash":      {"-c"},
	"sh":        {"-c"},
	"dash":      {"-c"},
	"zsh":       {"-c"},
	"nix":       {"-c", "--command"},
	"nix-shell": {"--run"},
	"env":       nil,
	"sudo":      nil,
	"doas":      nil,
	"nice":      nil,
	"nohup":     nil,
	"stdbuf":    nil,
}

// maxCommandWrapperDepth bounds wrapper recursion so a pathological command
// line cannot make classification unbounded work.
const maxCommandWrapperDepth = 4

// isVerificationCommand reports whether a command line runs a test, build,
// or static check. It inspects each pipeline and list segment so
// `cd repo && go test ./...` is recognized, and only the leading program of
// a segment, so a test name appearing inside an argument does not promote an
// unrelated command.
func isVerificationCommand(command string) bool {
	return verifiesCommand(command, 0)
}

// verifiesCommand scans a command line, recursing a bounded number of times
// through wrappers that run a nested command line.
func verifiesCommand(command string, depth int) bool {
	if depth > maxCommandWrapperDepth {
		return false
	}
	if len(command) > commandScanLimit {
		command = command[:commandScanLimit]
	}
	for _, segment := range commandSegments(command) {
		if verifiesSegment(segment, depth) {
			return true
		}
	}
	return false
}

// verifiesSegment matches one segment against the vocabulary, skipping
// leading environment assignments (`CGO_ENABLED=0 go test`) and comparing
// the program's base name so an absolute path still matches. When the
// program is a wrapper rather than a checker, the nested command line it
// carries is scanned instead.
func verifiesSegment(segment string, depth int) bool {
	fields := strings.Fields(segment)
	for len(fields) > 0 && isAssignment(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return false
	}
	program := unquote(fields[0])
	if i := strings.LastIndexByte(program, '/'); i >= 0 {
		program = program[i+1:]
	}
	if subcommands, known := verificationPrograms[program]; known {
		if subcommands == nil {
			return true
		}
		if matchesSubcommand(fields[1:], subcommands) {
			return true
		}
	}
	if triggers, wraps := commandWrappers[program]; wraps {
		if nested := wrappedCommand(fields[1:], triggers); nested != "" {
			return verifiesCommand(nested, depth+1)
		}
	}
	return false
}

// matchesSubcommand reports whether the program's first non-flag argument is
// an accepted subcommand. Anything after that argument belongs to the
// subcommand, not the program, so `go run ./cmd -test` never matches.
func matchesSubcommand(args, subcommands []string) bool {
	for _, arg := range args {
		for _, sub := range subcommands {
			if arg == sub {
				return true
			}
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return false
}

// wrappedCommand extracts the nested command line a wrapper runs. With
// triggers, it is everything after the trigger flag; without them, it starts
// at the first argument that is neither a flag nor an assignment.
func wrappedCommand(args, triggers []string) string {
	for i, arg := range args {
		if triggers == nil {
			if !isAssignment(arg) && !strings.HasPrefix(arg, "-") {
				return strings.Join(args[i:], " ")
			}
			continue
		}
		if matchesTrigger(arg, triggers) && i+1 < len(args) {
			return strings.Join(args[i+1:], " ")
		}
	}
	return ""
}

// matchesTrigger accepts a trigger exactly and, for the shell convention
// `-c`, also accepts it bundled with other short flags such as `-lc`.
func matchesTrigger(arg string, triggers []string) bool {
	for _, trigger := range triggers {
		if arg == trigger {
			return true
		}
		if trigger == "-c" && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg, 'c') {
			return true
		}
	}
	return false
}

func isAssignment(field string) bool {
	return strings.Contains(field, "=") && !strings.HasPrefix(field, "-")
}

// unquote strips the shell quoting a nested command line leaves on its
// program token, so `bash -c 'go test'` still names `go`.
func unquote(field string) string {
	return strings.Trim(field, `'"`)
}

// commandSegments splits a command line on the shell operators that start a
// new command. It is not a shell parser: quoting is ignored, which can only
// cause an extra segment to be inspected, never a missed one.
func commandSegments(command string) []string {
	segments := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(command); i++ {
		width := 0
		switch command[i] {
		case '&', '|':
			width = 1
			if i+1 < len(command) && command[i+1] == command[i] {
				width = 2
			}
		case ';', '\n', '(', ')', '{', '}':
			width = 1
		}
		if width == 0 {
			continue
		}
		segments = append(segments, command[start:i])
		i += width - 1
		start = i + 1
	}
	return append(segments, command[start:])
}
