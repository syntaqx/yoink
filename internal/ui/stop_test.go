package ui

import (
	"sync"
	"testing"
)

// Several goroutines stopping the same spinner at once used to be able to close its stop channel
// twice and panic.
func TestSpinnerStopFromManyGoroutines(t *testing.T) {
	for _, live := range []bool{true, false} {
		var buf syncBuffer
		s := StartSpinner(&buf, Painter{}, live, func(int) string { return "working" })
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				s.Stop()
			}()
		}
		close(start)
		wg.Wait()
		s.Stop() // and once more after all of them
	}
}
