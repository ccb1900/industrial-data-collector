package parallelism

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGateCapacityAndCancel(t *testing.T) {
	Configure(2) // 首次生效；本包其余测试共用同一容量
	ctx := context.Background()
	if !Acquire(ctx) || !Acquire(ctx) {
		t.Fatal("first two acquires must succeed at capacity 2")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if Acquire(cancelled) {
		t.Fatal("acquire on canceled ctx must return false")
	}
	Release()
	if !Acquire(ctx) {
		t.Fatal("slot freed by Release must be reacquirable")
	}
	// 共 3 次成功 acquire、已 Release 1 次：再还 2 个，闸恢复空。
	Release()
	Release()
}

func TestLedgerWaitAndDrain(t *testing.T) {
	Begin()
	Begin()
	waited := make(chan error, 1)
	go func() { waited <- Wait(context.Background()) }()
	select {
	case <-waited:
		t.Fatal("Wait returned with jobs still pending")
	case <-time.After(30 * time.Millisecond):
	}
	Finish(errors.New("boom"))
	select {
	case <-waited:
		t.Fatal("Wait returned while one job still pending")
	case <-time.After(30 * time.Millisecond):
	}
	Finish(nil)
	if err := <-waited; err != nil {
		t.Fatalf("Wait error = %v", err)
	}
	joined := DrainErrors()
	if joined == nil || !strings.Contains(joined.Error(), "boom") {
		t.Fatalf("DrainErrors = %v", joined)
	}
	if err := DrainErrors(); err != nil {
		t.Fatalf("second DrainErrors must be empty, got %v", err)
	}
}

func TestWaitHonorsContextCancel(t *testing.T) {
	Begin()
	defer Finish(nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want canceled", err)
	}
}
