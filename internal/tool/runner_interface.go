package tool

import "context"

// Runner executes subprocesses uniformly. It is satisfied by Run via
// NewRunner; tests may inject a fake.
type Runner interface {
	Run(ctx context.Context, name string, args []string, opts Options) (Result, error)
}

// runnerFunc adapts a function value to the Runner interface.
type runnerFunc func(context.Context, string, []string, Options) (Result, error)

// Run implements Runner.
func (f runnerFunc) Run(ctx context.Context, name string, args []string, opts Options) (Result, error) {
	return f(ctx, name, args, opts)
}

// NewRunner returns the default Runner backed by the package-level Run.
func NewRunner() Runner {
	return runnerFunc(Run)
}
