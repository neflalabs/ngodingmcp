package runner

// DefaultPrefixes and DefaultExact form the built-in run_command policy used
// when the feature is enabled without an explicit allowlist. They are kept in
// code (not in the workspace) so an agent cannot widen its own permissions.
//
// Prefix entries accept additional arguments; exact entries must match argv
// character for character. "ngodingmcp restart" (RestartCommand) is not run
// as a program; see Runner.Run and internal/selfrestart.
var (
	DefaultPrefixes = []string{
		"go build",
		"go test",
		"go vet",
		"gofmt -l",
		"staticcheck",
		"node",
		"git status",
		"git diff",
		"git log",
	}
	DefaultExact = []string{
		"bun init -y",
		"bun install",
		"bun install --frozen-lockfile",
		"bun test",
		"bun run build",
		"bun run dev",
		"npm init -y",
		"npm install",
		"npm ci",
		"npm test",
		"npm run build",
		"npm run dev",
		RestartCommand,
	}
)

// DefaultPolicy returns copies of the built-in allowlists.
func DefaultPolicy() (prefix, exact []string) {
	prefix = append([]string(nil), DefaultPrefixes...)
	exact = append([]string(nil), DefaultExact...)
	return prefix, exact
}
