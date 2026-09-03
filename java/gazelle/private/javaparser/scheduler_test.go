package javaparser

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/bazel-contrib/rules_jvm/java/gazelle/private/javaparser/proto/gazelle/java/javaparser/v0"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
)

func TestPrefetchUsesTheConfiguredWorkerCount(t *testing.T) {
	const workers = 8
	client := &queuedTestClient{started: make(chan string, 10), release: make(chan struct{}), blockAll: true}
	startQueuedPrefetch(t, client, workers, 10)
	for i := 0; i < workers; i++ {
		waitForQueuedPackage(t, client.started)
	}
	if got := client.peak.Load(); got != workers {
		t.Fatalf("active workers = %d, want %d", got, workers)
	}
}

func TestIdleWorkersTakeQueuedPackagesWhileAnotherWorkerIsBlocked(t *testing.T) {
	client := &queuedTestClient{started: make(chan string, 6), release: make(chan struct{})}
	startQueuedPrefetch(t, client, 2, 6)
	for i := 0; i < 6; i++ {
		waitForQueuedPackage(t, client.started)
	}
	if got := client.peak.Load(); got > 2 {
		t.Fatalf("active workers = %d, want at most 2", got)
	}
}

func TestOnDemandParsingSharesThePrefetchConcurrencyLimit(t *testing.T) {
	client := &queuedTestClient{started: make(chan string, 2), release: make(chan struct{}), blockAll: true}
	runner := startQueuedPrefetch(t, client, 2, 2)
	for i := 0; i < 2; i++ {
		waitForQueuedPackage(t, client.started)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.parsePackage(ctx, &pb.ParsePackageRequest{Rel: "mixed", Files: []string{"A.java", "B.kt"}})
	if err != context.Canceled || client.ordinaryCalls.Load() != 0 {
		t.Fatalf("queued on-demand parse: error = %v, RPC calls = %d", err, client.ordinaryCalls.Load())
	}
}

func startQueuedPrefetch(t *testing.T, client *queuedTestClient, workers, packages int) *Runner {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < packages; i++ {
		writeJavaFile(t, root, fmt.Sprintf("src/p%d/A.java", i))
	}
	runner := &Runner{rpc: client, logger: zerolog.Nop(), parserSlots: make(chan struct{}, workers)}
	runner.StartPrefetch(root, []SourceRoot{{Rel: "src", Recursive: true}}, 1)
	t.Cleanup(func() {
		close(client.release)
		runner.WaitForPrefetch()
		if got := client.peak.Load(); got > int32(workers) {
			t.Errorf("peak workers = %d, want at most %d", got, workers)
		}
		if len(runner.prefetch.packages) != packages {
			t.Errorf("prefetched %d packages, want %d", len(runner.prefetch.packages), packages)
		}
		for rel, result := range runner.prefetch.packages {
			if result.metadata.Name != rel {
				t.Errorf("metadata for %s belongs to %s", rel, result.metadata.Name)
			}
		}
	})
	return runner
}

func waitForQueuedPackage(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case pkg := <-started:
		return pkg
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not take a queued package")
		return ""
	}
}

type queuedTestClient struct {
	pb.JavaParserClient
	started       chan string
	release       chan struct{}
	blockAll      bool
	active        atomic.Int32
	peak          atomic.Int32
	ordinaryCalls atomic.Int32
}

func (c *queuedTestClient) ParseJavaPackages(_ context.Context, request *pb.ParseJavaPackagesRequest, _ ...grpc.CallOption) (*pb.ParseJavaPackagesResponse, error) {
	active := c.active.Add(1)
	defer c.active.Add(-1)
	for peak := c.peak.Load(); active > peak; peak = c.peak.Load() {
		if c.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	response := &pb.ParseJavaPackagesResponse{Packages: map[string]*pb.Package{}}
	for _, pkg := range request.Packages {
		c.started <- pkg.Rel
		if c.blockAll || pkg.Rel == "src/p0" {
			<-c.release
		}
		response.Packages[pkg.Rel] = &pb.Package{Name: pkg.Rel}
	}
	return response, nil
}

func (c *queuedTestClient) ParsePackage(context.Context, *pb.ParsePackageRequest, ...grpc.CallOption) (*pb.Package, error) {
	c.ordinaryCalls.Add(1)
	return &pb.Package{}, nil
}
