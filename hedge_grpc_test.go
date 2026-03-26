package hedge

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type testMsg struct{ V int }

func newTestConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///test-target", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func grpcWarmup(t *testing.T, interceptor grpc.UnaryClientInterceptor, cc *grpc.ClientConn, n int) {
	t.Helper()
	fast := func(_ context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		return nil
	}
	for i := 0; i < n; i++ {
		if err := interceptor(context.Background(), "/test.Svc/Do", &testMsg{}, &testMsg{}, cc, fast); err != nil {
			t.Fatalf("warmup %d: %v", i, err)
		}
	}
}

func TestGRPCNoHedgeWhenFast(t *testing.T) {
	var stats *Stats
	interceptor := NewUnaryClientInterceptor(
		WithStats(&stats),
		WithMinDelay(50*time.Millisecond),
		WithBudgetPercent(100),
	)
	cc := newTestConn(t)
	grpcWarmup(t, interceptor, cc, 25)

	fast := func(_ context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		time.Sleep(time.Millisecond)
		return nil
	}

	if err := interceptor(context.Background(), "/test.Svc/Do", &testMsg{}, &testMsg{}, cc, fast); err != nil {
		t.Fatal(err)
	}
	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d, want 0", n)
	}
}

func TestGRPCHedgeWhenSlow(t *testing.T) {
	var stats *Stats
	interceptor := NewUnaryClientInterceptor(
		WithStats(&stats),
		WithPercentile(0.5),
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
	)
	cc := newTestConn(t)
	grpcWarmup(t, interceptor, cc, 25)

	slow := func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		select {
		case <-time.After(200 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := interceptor(context.Background(), "/test.Svc/Do", &testMsg{}, &testMsg{}, cc, slow); err != nil {
		t.Fatal(err)
	}
	if n := stats.HedgedRequests.Load(); n == 0 {
		t.Error("expected HedgedRequests > 0")
	}
}

func TestGRPCContextCancellation(t *testing.T) {
	interceptor := NewUnaryClientInterceptor(
		WithBudgetPercent(100),
		WithMinDelay(time.Millisecond),
	)
	cc := newTestConn(t)

	blocked := func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- interceptor(ctx, "/test.Svc/Do", &testMsg{}, &testMsg{}, cc, blocked)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interceptor did not return after context cancellation")
	}
}
