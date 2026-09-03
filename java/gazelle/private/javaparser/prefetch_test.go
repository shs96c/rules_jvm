package javaparser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/bazel-contrib/rules_jvm/java/gazelle/private/javaparser/proto/gazelle/java/javaparser/v0"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
)

func TestDiscoverJavaPackagesRespectsScope(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"src/A.java", "src/B.kt", "src/child/B.java", "other/C.java", "src/bazel-out/Generated.java"} {
		writeJavaFile(t, root, file)
	}
	if err := os.Symlink(filepath.Join(root, "other"), filepath.Join(root, "src", "linked")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		roots []SourceRoot
		want  map[string][]string
	}{
		{"directory", []SourceRoot{{Rel: "src"}}, map[string][]string{"src": {"A.java"}}},
		{"recursive", []SourceRoot{{Rel: "src", Recursive: true}}, map[string][]string{"src": {"A.java"}, "src/child": {"B.java"}}},
		{"overlapping", []SourceRoot{{Rel: "src", Recursive: true}, {Rel: "src/child", Recursive: true}}, map[string][]string{"src": {"A.java"}, "src/child": {"B.java"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			packages, err := discoverJavaPackages(root, test.roots)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string][]string{}
			for _, request := range packages {
				got[request.Rel] = request.Files
			}
			if !reflect.DeepEqual(test.want, got) {
				t.Fatalf("packages = %v, want %v", got, test.want)
			}
			if len(packages) != len(test.want) {
				t.Fatal("duplicate package requests")
			}
		})
	}
}

func TestPrefetchOnlyReusesTheExactJavaFileSelection(t *testing.T) {
	root := t.TempDir()
	writeJavaFile(t, root, "src/A.java")
	writeJavaFile(t, root, "src/B.java")
	client := &prefetchTestClient{}
	runner := &Runner{rpc: client, logger: zerolog.Nop()}
	runner.StartPrefetch(root, []SourceRoot{{Rel: "src", Recursive: true}}, 1)
	runner.WaitForPrefetch()

	for _, test := range []struct {
		name        string
		files       []string
		wantPackage string
	}{
		{"exact", []string{"A.java", "B.java"}, "prefetched"},
		{"reordered", []string{"B.java", "A.java"}, "prefetched"},
		{"excluded file", []string{"A.java"}, "ordinary"},
		{"mixed Kotlin", []string{"A.java", "B.java", "C.kt"}, "ordinary"},
		{"new file", []string{"A.java", "B.java", "C.java"}, "ordinary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := runner.ParsePackage(context.Background(), &ParsePackageRequest{Rel: "src", Files: test.files})
			if err != nil {
				t.Fatal(err)
			}
			if result.Name.Name != test.wantPackage {
				t.Fatalf("package = %q, want %q", result.Name.Name, test.wantPackage)
			}
			if result.Files.Len() != len(test.files) {
				t.Fatal("requested files were lost")
			}
		})
	}
	if got := client.ordinaryCalls.Load(); got != 4 {
		t.Fatalf("ordinary calls = %d, want 4", got)
	}
}

func TestPrefetchFailureFallsBackToOrdinaryParsing(t *testing.T) {
	for _, failRPC := range []bool{false, true} {
		t.Run(fmt.Sprint(failRPC), func(t *testing.T) {
			root := t.TempDir()
			writeJavaFile(t, root, "src/A.java")
			client := &prefetchTestClient{failRPC: failRPC, omitPackages: true}
			runner := &Runner{rpc: client, logger: zerolog.Nop()}
			runner.StartPrefetch(root, []SourceRoot{{Rel: "src", Recursive: true}}, 1)
			runner.WaitForPrefetch()
			result, err := runner.ParsePackage(context.Background(), &ParsePackageRequest{Rel: "src", Files: []string{"A.java"}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Name.Name != "ordinary" || client.ordinaryCalls.Load() != 1 {
				t.Fatal("failed prefetch did not fall back")
			}
		})
	}
}

func TestPrefetchBatchesRequestsWithoutPersistingResults(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		writeJavaFile(t, root, fmt.Sprintf("src/p%d/A.java", i))
	}
	client := &prefetchTestClient{}
	for run := 0; run < 2; run++ {
		runner := &Runner{rpc: client, logger: zerolog.Nop()}
		runner.StartPrefetch(root, []SourceRoot{{Rel: "src", Recursive: true}}, 2)
		runner.WaitForPrefetch()
	}
	if got := client.batchCalls.Load(); got != 6 {
		t.Fatalf("batch calls = %d, want 6", got)
	}
	if got := client.largestBatch.Load(); got > 2 {
		t.Fatalf("batch contains %d packages, want at most 2", got)
	}
}

func TestDisabledPrefetchUsesOrdinaryParsing(t *testing.T) {
	client := &prefetchTestClient{}
	runner := &Runner{rpc: client, logger: zerolog.Nop()}
	runner.StartPrefetch(t.TempDir(), []SourceRoot{{Recursive: true}}, 0)
	result, err := runner.ParsePackage(context.Background(), &ParsePackageRequest{Files: []string{"A.java"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name.Name != "ordinary" || client.batchCalls.Load() != 0 {
		t.Fatal("disabled prefetch did not use ordinary parsing")
	}
}

func TestPrefetchedPackagesKeepTheParserAlive(t *testing.T) {
	root := t.TempDir()
	writeJavaFile(t, root, "src/A.java")
	client := &prefetchTestClient{}
	runner := &Runner{rpc: client, logger: zerolog.Nop()}
	runner.StartPrefetch(root, []SourceRoot{{Rel: "src"}}, 1)
	runner.WaitForPrefetch()
	runner.prefetch.started = time.Now().Add(-time.Minute)
	for i := 0; i < 2; i++ {
		result, err := runner.ParsePackage(context.Background(), &ParsePackageRequest{Rel: "src", Files: []string{"A.java"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Name.Name != "prefetched" {
			t.Fatal("keepalive discarded prefetched metadata")
		}
	}
	if got := client.keepAliveCalls.Load(); got != 1 {
		t.Fatalf("keepalive calls = %d, want 1", got)
	}
}

func TestWaitingForPrefetchRespectsCancellation(t *testing.T) {
	runner := &Runner{prefetch: &prefetch{done: make(chan struct{})}, logger: zerolog.Nop()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.parsePackage(ctx, &pb.ParsePackageRequest{Files: []string{"A.java"}}); err != context.Canceled {
		t.Fatalf("cancelled parse returned %v", err)
	}
}

func TestMixedPackagesReuseOnlyExactCompletedJavaResults(t *testing.T) {
	for _, test := range []struct {
		name                       string
		files                      []string
		javaName, kotlinName       string
		pending, missing, disabled bool
		wantRequests               [][]string
	}{
		{name: "exact", files: []string{"A.java", "B.java", "C.kt"}, javaName: "example", kotlinName: "example", wantRequests: [][]string{{"C.kt"}}},
		{name: "reordered", files: []string{"C.kt", "B.java", "A.java"}, javaName: "example", kotlinName: "example", wantRequests: [][]string{{"C.kt"}}},
		{name: "excluded Java file", files: []string{"A.java", "C.kt"}, javaName: "example", kotlinName: "example"},
		{name: "new Java file", files: []string{"A.java", "B.java", "New.java", "C.kt"}, javaName: "example", kotlinName: "example"},
		{name: "unfinished prefetch", files: []string{"A.java", "B.java", "C.kt"}, javaName: "example", kotlinName: "example", pending: true},
		{name: "failed or missing prefetch", files: []string{"A.java", "B.java", "C.kt"}, javaName: "example", kotlinName: "example", missing: true},
		{name: "disabled prefetch", files: []string{"A.java", "B.java", "C.kt"}, javaName: "example", kotlinName: "example", disabled: true},
		{name: "unnamed Java package", files: []string{"A.java", "B.java", "C.kt"}, kotlinName: "example"},
		{name: "unnamed Kotlin package", files: []string{"A.java", "B.java", "C.kt"}, javaName: "example", wantRequests: [][]string{{"C.kt"}, {"A.java", "B.java", "C.kt"}}},
		{name: "different packages", files: []string{"A.java", "B.java", "C.kt"}, javaName: "example", kotlinName: "other", wantRequests: [][]string{{"C.kt"}, {"A.java", "B.java", "C.kt"}}},
		{name: "other files", files: []string{"A.java", "B.java", "C.kt", "D.scala"}, javaName: "example", kotlinName: "example"},
		{name: "Kotlin only", files: []string{"C.kt"}, javaName: "example", kotlinName: "example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &mixedPackageClient{metadata: &pb.Package{Name: test.kotlinName}}
			pending := &prefetch{done: make(chan struct{}), packages: map[string]prefetchedPackage{}}
			if !test.missing {
				pending.packages["src"] = prefetchedPackage{files: []string{"A.java", "B.java"}, metadata: &pb.Package{Name: test.javaName}}
			}
			if !test.pending {
				close(pending.done)
			}
			runner := Runner{rpc: client, prefetch: pending, logger: zerolog.Nop()}
			if test.disabled {
				runner.prefetch = nil
			}
			originalFiles := append([]string(nil), test.files...)
			result, err := runner.ParsePackage(context.Background(), &ParsePackageRequest{Rel: "src", Files: test.files})
			if err != nil {
				t.Fatal(err)
			}
			want := test.wantRequests
			if want == nil {
				want = [][]string{test.files}
			}
			if !reflect.DeepEqual(client.requests, want) {
				t.Fatalf("requests = %v, want %v", client.requests, want)
			}
			if !reflect.DeepEqual(test.files, originalFiles) || result.Files.Len() != len(test.files) {
				t.Fatal("original file selection changed")
			}
		})
	}
}

func TestMixedPackageMetadataIsUnionedWithoutChangingInputs(t *testing.T) {
	javaData := packageMetadata("example.Java")
	kotlinData := packageMetadata("example.Kotlin")
	pending := &prefetch{
		done:     make(chan struct{}),
		packages: map[string]prefetchedPackage{"src": {files: []string{"A.java"}, metadata: javaData}},
	}
	close(pending.done)
	client := &mixedPackageClient{metadata: kotlinData}
	runner := Runner{rpc: client, prefetch: pending}
	result, err := runner.parsePackage(context.Background(), &pb.ParsePackageRequest{Rel: "src", Files: []string{"A.java", "B.kt"}})
	if err != nil {
		t.Fatal(err)
	}
	expected := packageMetadata("example.Java", "example.Kotlin")
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("metadata = %v, want %v", result, expected)
	}
	result.ImportedClasses[0] = "changed"
	result.PerClassMetadata["example.Shared"].AnnotationClassNames[0] = "changed"
	result.PerClassMetadata["example.Shared"].PerMethodMetadata["method"].AnnotationClassNames[0] = "changed"
	result.PerClassMetadata["example.Shared"].PerFieldMetadata["field"].AnnotationClassNames[0] = "changed"
	if !reflect.DeepEqual(javaData, packageMetadata("example.Java")) || !reflect.DeepEqual(kotlinData, packageMetadata("example.Kotlin")) {
		t.Fatal("merging or using the result changed an input")
	}
}

func TestMixedPackagePreservesCombinedValidationErrors(t *testing.T) {
	for _, kotlinError := range []error{nil, fmt.Errorf("Kotlin-only package validation failed")} {
		t.Run(fmt.Sprint(kotlinError), func(t *testing.T) {
			client := &mixedPackageClient{
				metadata:      &pb.Package{Name: "other"},
				combinedError: fmt.Errorf("expected exactly one Java package"),
				kotlinError:   kotlinError,
			}
			pending := &prefetch{done: make(chan struct{}), packages: map[string]prefetchedPackage{
				"src": {files: []string{"A.java"}, metadata: &pb.Package{Name: "example"}},
			}}
			close(pending.done)
			runner := Runner{rpc: client, prefetch: pending}
			_, err := runner.parsePackage(context.Background(), &pb.ParsePackageRequest{Rel: "src", Files: []string{"A.java", "B.kt"}})
			if err != client.combinedError {
				t.Fatalf("error = %v, want the combined parser's error", err)
			}
		})
	}
}

func packageMetadata(names ...string) *pb.Package {
	values := append(append([]string(nil), names...), "example.Shared")
	return &pb.Package{
		Name:                                   "example",
		ImportedClasses:                        append([]string(nil), values...),
		ImportedPackagesWithoutSpecificClasses: append([]string(nil), values...),
		ExportedClasses:                        append([]string(nil), values...),
		InternalClasses:                        append([]string(nil), values...),
		DeclaredClasses:                        append([]string(nil), values...),
		Mains:                                  append([]string(nil), values...),
		PerClassMetadata: map[string]*pb.PerClassMetadata{
			"example.Shared": {
				AnnotationClassNames: append([]string(nil), values...),
				PerMethodMetadata:    map[string]*pb.PerMethodMetadata{"method": {AnnotationClassNames: append([]string(nil), values...)}},
				PerFieldMetadata:     map[string]*pb.PerFieldMetadata{"field": {AnnotationClassNames: append([]string(nil), values...)}},
			},
		},
	}
}

type mixedPackageClient struct {
	pb.JavaParserClient
	metadata      *pb.Package
	requests      [][]string
	combinedError error
	kotlinError   error
}

func (c *mixedPackageClient) ParsePackage(_ context.Context, request *pb.ParsePackageRequest, _ ...grpc.CallOption) (*pb.Package, error) {
	c.requests = append(c.requests, append([]string(nil), request.Files...))
	if len(request.Files) > 1 && c.combinedError != nil {
		return nil, c.combinedError
	}
	return c.metadata, c.kotlinError
}

func writeJavaFile(t *testing.T, root, relative string) {
	t.Helper()
	file := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("class Example {}"), 0644); err != nil {
		t.Fatal(err)
	}
}

type prefetchTestClient struct {
	pb.JavaParserClient
	ordinaryCalls  atomic.Int32
	keepAliveCalls atomic.Int32
	batchCalls     atomic.Int32
	largestBatch   atomic.Int32
	omitPackages   bool
	failRPC        bool
}

func (c *prefetchTestClient) ParsePackage(_ context.Context, request *pb.ParsePackageRequest, _ ...grpc.CallOption) (*pb.Package, error) {
	if len(request.Files) == 0 {
		c.keepAliveCalls.Add(1)
		return &pb.Package{}, nil
	}
	c.ordinaryCalls.Add(1)
	return &pb.Package{Name: "ordinary"}, nil
}

func (c *prefetchTestClient) ParseJavaPackages(_ context.Context, request *pb.ParseJavaPackagesRequest, _ ...grpc.CallOption) (*pb.ParseJavaPackagesResponse, error) {
	c.batchCalls.Add(1)
	size := int32(len(request.Packages))
	for old := c.largestBatch.Load(); size > old; old = c.largestBatch.Load() {
		if c.largestBatch.CompareAndSwap(old, size) {
			break
		}
	}
	if c.failRPC {
		return nil, fmt.Errorf("batch failed")
	}
	response := &pb.ParseJavaPackagesResponse{Packages: map[string]*pb.Package{}}
	if !c.omitPackages {
		for _, pkg := range request.Packages {
			response.Packages[pkg.Rel] = &pb.Package{Name: "prefetched"}
		}
	}
	return response, nil
}
