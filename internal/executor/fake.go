package executor

import (
	"context"
	"fmt"
	"sync"
)

type FakeExecutor struct {
	events []ExecutionEvent
}

func NewFakeExecutor(events []ExecutionEvent) *FakeExecutor {
	return &FakeExecutor{events: append([]ExecutionEvent(nil), events...)}
}

func (f *FakeExecutor) Start(ctx context.Context, request ExecutionRequest) (ExecutionHandle, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	for _, event := range f.events {
		if event.RunID != request.RunID {
			return nil, fmt.Errorf("fake event run id %q does not match request %q", event.RunID, request.RunID)
		}
	}
	handle := &fakeHandle{events: make(chan ExecutionEvent), cancelled: make(chan struct{}), done: make(chan struct{})}
	go handle.emit(ctx, append([]ExecutionEvent(nil), f.events...))
	return handle, nil
}

type fakeHandle struct {
	events    chan ExecutionEvent
	cancelled chan struct{}
	done      chan struct{}
	once      sync.Once
}

func (h *fakeHandle) Events(context.Context) <-chan ExecutionEvent { return h.events }

func (h *fakeHandle) Cancel(context.Context) error {
	h.once.Do(func() {
		close(h.cancelled)
		<-h.done
	})
	return nil
}

func (h *fakeHandle) emit(ctx context.Context, events []ExecutionEvent) {
	defer close(h.done)
	defer close(h.events)
	for _, event := range events {
		select {
		case h.events <- event:
		case <-h.cancelled:
			return
		case <-ctx.Done():
			return
		}
	}
}

var _ ExecutorAdapter = (*FakeExecutor)(nil)
