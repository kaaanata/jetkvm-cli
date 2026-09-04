package setup

import "context"

// Runner is the only process execution boundary. Implementations must not
// interpret a non-zero exit as success or inject credentials into commands.
type Runner interface {
	Run(context.Context, Command) (CommandResult, error)
}
