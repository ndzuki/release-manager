package e2e_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ndzuki/release-manager/test/e2e"
)

func TestCompensationRegistryRunsExactlyOnceInLIFOOrder(t *testing.T) {
	t.Parallel()

	registry := e2e.NewCompensationRegistry()
	var order []string
	for _, id := range []string{"first", "second", "third"} {
		id := id
		if err := registry.Register(id, func(context.Context) error {
			order = append(order, id)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"third", "second", "first"}) {
		t.Fatalf("order = %#v, want reverse registration order", order)
	}
	if err := registry.Register("late", func(context.Context) error { return nil }); !errors.Is(err, e2e.ErrCompensationClosed) {
		t.Fatalf("late registration = %v, want ErrCompensationClosed", err)
	}
}

func TestCompensationRegistryAttemptsAllAndReturnsDirty(t *testing.T) {
	t.Parallel()

	registry := e2e.NewCompensationRegistry()
	called := make(chan string, 2)
	if err := registry.Register("first", func(context.Context) error {
		called <- "first"
		return errors.New("first failed")
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("second", func(context.Context) error {
		called <- "second"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Run(context.Background()); !errors.Is(err, e2e.ErrCompensationDirty) {
		t.Fatalf("run error = %v, want ErrCompensationDirty", err)
	}
	close(called)
	var got []string
	for id := range called {
		got = append(got, id)
	}
	if !reflect.DeepEqual(got, []string{"second", "first"}) {
		t.Fatalf("called = %#v, want both actions in LIFO order", got)
	}
}
