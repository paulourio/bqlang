package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sync/semaphore"
)

// A sequencer performs concurrent tasks that may write output, emitting that
// output in a deterministic order.
type sequencer struct {
	maxWeight int64
	sem       *semaphore.Weighted
	prev      <-chan *reporterState
}

// A reporter reports output, warnings, and errors.
type reporter struct {
	prev  <-chan *reporterState
	state *reporterState
}

// reporterState carries the state of a reporter instance.
// Only one reporter at a time may have access to a reporterState.
type reporterState struct {
	out, errOut io.Writer
	exitCode    int
}

// exclusive is a weight that causes a task to run without concurrent tasks.
const exclusive = -1

// newSequencer returns a sequencer that allows concurrent tasks up to maxWeight
// and writes tasks' output to out and errOut.
func newSequencer(maxWeight int64, out, errOut io.Writer) *sequencer {
	sem := semaphore.NewWeighted(maxWeight)

	prev := make(chan *reporterState, 1)
	prev <- &reporterState{out: out, errOut: errOut}

	return &sequencer{
		maxWeight: maxWeight,
		sem:       sem,
		prev:      prev,
	}
}

func fileWeight(path string, info fs.FileInfo) int64 {
	if info == nil {
		return exclusive
	}

	if info.Mode().Type() == fs.ModeSymlink {
		var err error

		info, err = os.Stat(path)
		if err != nil {
			return exclusive
		}
	}

	if !info.Mode().IsRegular() {
		return exclusive
	}

	return info.Size()
}

// Add blocks until the sequencer has enough weight to spare, then runs f.
func (s *sequencer) Add(weight int64, f func(*reporter) error) {
	if weight < 0 || weight > s.maxWeight {
		weight = s.maxWeight
	}

	if err := s.sem.Acquire(context.Background(), weight); err != nil {
		weight = 0
		f = func(*reporter) error { return fmt.Errorf("acquire semaphore: %w", err) }
	}

	r := &reporter{prev: s.prev}
	next := make(chan *reporterState, 1)
	s.prev = next

	go func() {
		if err := f(r); err != nil {
			r.Report(err)
		}

		next <- r.getState()

		s.sem.Release(weight)
	}()
}

// AddReport prints an error to s after previously-added tasks.
func (s *sequencer) AddReport(err error) {
	s.Add(0, func(*reporter) error { return err })
}

// GetExitCode waits for all tasks to complete and returns the process exit code.
func (s *sequencer) GetExitCode() int {
	c := make(chan int, 1)

	s.Add(0, func(r *reporter) error {
		c <- r.ExitCode()

		return nil
	})

	return <-c
}

func (r *reporter) getState() *reporterState {
	if r.state == nil {
		r.state = <-r.prev
	}

	return r.state
}

// Warnf emits a warning message without changing the exit code.
func (r *reporter) Warnf(format string, args ...any) {
	fmt.Fprintf(r.getState().errOut, format, args...)
}

// Write emits bytes to the reporter's output stream.
func (r *reporter) Write(p []byte) (int, error) {
	n, err := r.getState().out.Write(p)
	if err != nil {
		return n, fmt.Errorf("reporter write: %w", err)
	}

	return n, nil
}

// MarkDiff records a formatting difference, setting exit code 1 unless a
// higher-severity error has already been recorded.
func (r *reporter) MarkDiff() {
	st := r.getState()
	if st.exitCode < 1 {
		st.exitCode = 1
	}
}

// Report emits a non-nil error and sets exit code 2.
func (r *reporter) Report(err error) {
	if err == nil {
		panic("Report with nil error")
	}

	st := r.getState()
	fmt.Fprintln(st.errOut, err)
	st.exitCode = 2
}

// ExitCode returns the accumulated exit code.
func (r *reporter) ExitCode() int {
	return r.getState().exitCode
}
