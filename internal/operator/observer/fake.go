package observer

import (
	"context"
	"sync"
	"time"
)

type FakeBehavior int

const (
	FakeImmediateReady FakeBehavior = iota
	FakeDelayedReady
	FakeNeverReady
	FakeError
)

type FakeResponse struct {
	Behavior FakeBehavior
	Delay    time.Duration
	Result   WatchResult
	Err      error
}

type FakeObserver struct {
	mu        sync.Mutex
	responses map[ResourceRef]FakeResponse
	calls     []ResourceRef
}

func NewFake() *FakeObserver {
	return &FakeObserver{responses: make(map[ResourceRef]FakeResponse)}
}

func (f *FakeObserver) SetResponse(ref ResourceRef, response FakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[ref] = response
}

func (f *FakeObserver) Calls() []ResourceRef {
	f.mu.Lock()
	defer f.mu.Unlock()

	calls := make([]ResourceRef, len(f.calls))
	copy(calls, f.calls)
	return calls
}

func (f *FakeObserver) Observe(
	ctx context.Context,
	ref ResourceRef,
	_ int64,
	timeout time.Duration,
) (WatchResult, error) {
	return f.ObserveWithProgress(ctx, ref, 0, timeout, nil)
}

func (f *FakeObserver) ObserveWithProgress(
	ctx context.Context,
	ref ResourceRef,
	_ int64,
	timeout time.Duration,
	onProgress func(WatchResult),
) (WatchResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, ref)
	response, ok := f.responses[ref]
	f.mu.Unlock()

	if !ok {
		response = FakeResponse{Behavior: FakeImmediateReady}
	}

	result := response.Result
	result.Resource = ref

	report := func() {
		if onProgress != nil {
			onProgress(result)
		}
	}

	switch response.Behavior {
	case FakeImmediateReady:
		result.Ready = true
		report()
		return result, nil

	case FakeDelayedReady:
		delay := response.Delay
		if delay <= 0 {
			delay = time.Millisecond
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			result.Ready = true
			report()
			return result, nil
		case <-ctx.Done():
			report()
			return result, &RolloutError{Kind: ErrCancelled, Last: result, Err: ctx.Err()}
		}

	case FakeNeverReady:
		observeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		<-observeCtx.Done()
		report()
		if ctx.Err() != nil {
			return result, &RolloutError{Kind: ErrCancelled, Last: result, Err: ctx.Err()}
		}
		return result, &RolloutError{Kind: ErrRolloutTimeout, Last: result, Err: observeCtx.Err()}

	case FakeError:
		injectedErr := response.Err
		if injectedErr == nil {
			injectedErr = ErrWatchDisconnected
		}
		report()
		return result, &RolloutError{Kind: injectedErr, Last: result, Err: injectedErr}

	default:
		report()
		return result, response.Err
	}
}

var _ RolloutObserver = (*FakeObserver)(nil)
