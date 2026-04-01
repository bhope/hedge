package hedge

import (
	"context"
	"fmt"
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
	t.Cleanup(func() { _ = cc.Close() })
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

func TestGRPCHedgePerKey(t *testing.T) {
	var stats *Stats
	interceptor := NewUnaryClientInterceptor(
		WithStats(&stats),
		WithPercentile(0.5),
		WithBudgetPercent(100),
		WithMinDelay(10*time.Millisecond),
		WithGRPCKeyFunc(func(d GRPCRequestData) string {
			return fmt.Sprintf("%s/%s", d.Hostname, d.Method)
		}),
	)
	cc := newTestConn(t)
	fooDelay := 5 * time.Millisecond
	barDelay := 20 * time.Millisecond
	invoker := func(ctx context.Context, method string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		var delay time.Duration

		switch method {
		case "/test.Svc/Foo":
			delay = fooDelay
		case "/test.Svc/Bar":
			delay = barDelay
		default:
			return nil
		}
		select {
		case <-time.After(delay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// warm up both keys
	for i := 0; i < 25; i++ {
		if err := interceptor(context.Background(), "/test.Svc/Foo", &testMsg{}, &testMsg{}, cc, invoker); err != nil {
			t.Fatalf("foo warmup %d: %v", i, err)
		}
		if err := interceptor(context.Background(), "/test.Svc/Bar", &testMsg{}, &testMsg{}, cc, invoker); err != nil {
			t.Fatalf("bar warmup %d: %v", i, err)
		}
	}

	*stats = Stats{} // reset stats after warmup

	if err := interceptor(context.Background(), "/test.Svc/Foo", &testMsg{}, &testMsg{}, cc, invoker); err != nil {
		t.Fatal(err)
	}
	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d for fast /test.Svc/Foo, want 0", n)
	}

	if err := interceptor(context.Background(), "/test.Svc/Bar", &testMsg{}, &testMsg{}, cc, invoker); err != nil {
		t.Fatal(err)
	}
	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d for fast /test.Svc/Bar, want 0", n)
	}

	// swap foo and bar response delays
	fooDelay, barDelay = barDelay, fooDelay

	// bar requests should not hedge because they got faster
	if err := interceptor(context.Background(), "/test.Svc/Bar", &testMsg{}, &testMsg{}, cc, invoker); err != nil {
		t.Fatal(err)
	}
	if n := stats.HedgedRequests.Load(); n != 0 {
		t.Errorf("HedgedRequests = %d for /test.Svc/Bar, want 0", n)
	}

	// foo requests should hedge because they are slower than before
	if err := interceptor(context.Background(), "/test.Svc/Foo", &testMsg{}, &testMsg{}, cc, invoker); err != nil {
		t.Fatal(err)
	}
	if n := stats.HedgedRequests.Load(); n == 0 {
		t.Error("expected HedgedRequests > 0 for slowed down /test.Svc/Foo")
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
