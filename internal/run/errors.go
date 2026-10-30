package run

import "fmt"

type TransitionError struct{ From, To Status }

func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid run transition %s -> %s", e.From, e.To)
}
