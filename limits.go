package gopherbox

import (
	"errors"
	"time"
)

var (
	ErrTimeoutExceeded      = errors.New("gopherbox: execution timed out")
	ErrCommandLimitExceeded = errors.New("gopherbox: max command count exceeded")
	ErrOutputLimitExceeded  = errors.New("gopherbox: max output size exceeded")
	ErrLoopLimitExceeded    = errors.New("gopherbox: max loop iterations exceeded")
	ErrCallDepthExceeded    = errors.New("gopherbox: max function call depth exceeded")
)

// ExecutionLimits defines execution bounds for a script run.
type ExecutionLimits struct {
	MaxTimeout      time.Duration // Per-exec timeout. Default: 30s
	MaxLoopIter     int           // Max iterations per loop. Default: 10000
	MaxCallDepth    int           // Max function recursion. Default: 50
	MaxCommandCount int           // Max total commands per exec. Default: 10000
	MaxOutputBytes  int           // Max stdout+stderr bytes. Default: 1MB
}

func (l ExecutionLimits) withDefaults() ExecutionLimits {
	if l.MaxTimeout <= 0 {
		l.MaxTimeout = 30 * time.Second
	}
	if l.MaxLoopIter <= 0 {
		l.MaxLoopIter = 10_000
	}
	if l.MaxCallDepth <= 0 {
		l.MaxCallDepth = 50
	}
	if l.MaxCommandCount <= 0 {
		l.MaxCommandCount = 10_000
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = 1 << 20 // 1 MiB
	}
	return l
}
