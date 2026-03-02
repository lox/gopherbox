package gopherbox

import gcmd "github.com/buildkite/gopherbox/commands"

func defaultCommands() map[string]CommandFunc {
	return gcmd.DefaultCommands()
}
