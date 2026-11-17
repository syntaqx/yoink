package mirror

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func tasks(n int) []Task {
	out := make([]Task, n)
	for i := range out {
		out[i] = Task{Name: strconv.Itoa(i)}
	}
	return out
}

func TestRunLimitsConcurrency(t *testing.T) {
	for _, c := range []struct{ jobs, want int }{{3, 3}, {1, 1}, {0, 1}, {-2, 1}} {
		t.Run(fmt.Sprintf("jobs=%d", c.jobs), func(t *testing.T) {
			var inFlight, peak atomic.Int32
			full := make(chan struct{}, 1)
			release := make(chan struct{})
			work := func(_ context.Context, task Task) Result {
				n := inFlight.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				if int(n) == c.want {
					select {
					case full <- struct{}{}:
					default:
					}
				}
				<-release
				inFlight.Add(-1)
				return Result{Task: task}
			}

			var inDone atomic.Bool
			seen := map[string]bool{}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				Run(context.Background(), tasks(12), c.jobs, work, func(r Result) {
					if !inDone.CompareAndSwap(false, true) {
						t.Error("done called concurrently")
					}
					seen[r.Task.Name] = true
					inDone.Store(false)
				})
			}()

			select {
			case <-full:
			case <-time.After(5 * time.Second):
				t.Fatalf("never reached %d tasks at once", c.want)
			}
			close(release)
			<-finished
			if got := int(peak.Load()); got != c.want {
				t.Errorf("peak concurrency = %d, want %d", got, c.want)
			}
			if len(seen) != 12 {
				t.Errorf("done called for %d of 12 tasks", len(seen))
			}
		})
	}
}

func TestRunStopsQueueingWhenCanceled(t *testing.T) {
	for _, jobs := range []int{1, 4} {
		ctx, cancel := context.WithCancel(context.Background())
		var worked, done atomic.Int32
		Run(ctx, tasks(200), jobs, func(_ context.Context, task Task) Result {
			worked.Add(1)
			cancel()
			return Result{Task: task}
		}, func(Result) { done.Add(1) })
		cancel()

		if n := worked.Load(); n == 0 || n >= 200 {
			t.Errorf("jobs=%d: worked on %d of 200 tasks after canceling on the first", jobs, n)
		}
		// Every task that was started is reported; nothing is lost or duplicated.
		if done.Load() != worked.Load() {
			t.Errorf("jobs=%d: %d done for %d worked", jobs, done.Load(), worked.Load())
		}
	}
}

func TestRunWithNoTasks(t *testing.T) {
	Run(context.Background(), nil, 4, func(context.Context, Task) Result {
		t.Error("work called with no tasks")
		return Result{}
	}, func(Result) { t.Error("done called with no tasks") })
}
