package javaparser

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	pb "github.com/bazel-contrib/rules_jvm/java/gazelle/private/javaparser/proto/gazelle/java/javaparser/v0"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
)

func cacheForTest(t *testing.T, dir, root, identity string, budget int64) *parseCache {
	t.Helper()
	c, err := openParseCache(dir, root, identity, budget)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func cacheKeyForTest(t *testing.T, c *parseCache, files ...string) cacheKey {
	t.Helper()
	key, err := c.key(&pb.ParsePackageRequest{Rel: "src", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestParseCachePersistsIndependentMetadata(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	writeJavaFile(t, root, "src/A.java")
	c := cacheForTest(t, dir, root, "parser-v1", 32<<20)
	key := cacheKeyForTest(t, c, "A.java")
	want := &pb.Package{Name: "example", ImportedClasses: []string{"other.Type"}}
	c.put(key, want)
	want.ImportedClasses[0] = "mutated"
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	c = cacheForTest(t, dir, root, "parser-v1", 32<<20)
	got, ok := c.get(key)
	if !ok || got.ImportedClasses[0] != "other.Type" {
		t.Fatalf("cached metadata = %v, %v", got, ok)
	}
	got.ImportedClasses[0] = "also mutated"
	again, _ := c.get(key)
	if again.ImportedClasses[0] != "other.Type" {
		t.Fatal("cache returned mutable shared metadata")
	}
}

func TestParseCacheKeysTrackContentSelectionAndParser(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	writeJavaFile(t, root, "src/A.java")
	writeJavaFile(t, root, "src/B.kt")
	c := cacheForTest(t, dir, root, "v1", 32<<20)
	original := cacheKeyForTest(t, c, "A.java", "B.kt")
	if original == cacheKeyForTest(t, c, "B.kt", "A.java") {
		t.Fatal("request order was ignored")
	}
	if original == cacheKeyForTest(t, c, "A.java") {
		t.Fatal("selection was ignored")
	}
	if original == cacheKeyForTest(t, cacheForTest(t, dir, root, "v2", 32<<20), "A.java", "B.kt") {
		t.Fatal("parser identity was ignored")
	}
	file := filepath.Join(root, "src/A.java")
	info, _ := os.Stat(file)
	content, _ := os.ReadFile(file)
	content[0] ^= 1
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if original == cacheKeyForTest(t, c, "A.java", "B.kt") {
		t.Fatal("same-size edit with preserved mtime was ignored")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := c.key(&pb.ParsePackageRequest{Rel: "src", Files: []string{"A.java"}}); err == nil {
		t.Fatal("deleted file was accepted")
	}
}

func TestParseCacheCorruptionIsAMiss(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	writeJavaFile(t, root, "src/A.java")
	c := cacheForTest(t, dir, root, "v1", 32<<20)
	key := cacheKeyForTest(t, c, "A.java")
	c.put(key, &pb.Package{Name: "example"})
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(filepath.Join(dir, "metadata"), data, 0600); err != nil {
		t.Fatal(err)
	}
	c = cacheForTest(t, dir, root, "v1", 32<<20)
	if _, ok := c.get(key); ok {
		t.Fatal("corrupt metadata was reused")
	}
}

func noisyPackage(seed int64, n int) *pb.Package {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + r.Intn(26))
	}
	return &pb.Package{Name: "example", ImportedClasses: []string{string(b)}}
}

func TestParseCacheEvictsOldEntriesWithinTheBudget(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	c := cacheForTest(t, dir, root, "v1", 12<<10)
	a, b := cacheKey{1}, cacheKey{2}
	c.put(a, noisyPackage(1, 6000))
	c.put(b, noisyPackage(2, 6000))
	if _, ok := c.get(a); !ok {
		t.Fatal("new entry missing")
	}
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	c = cacheForTest(t, dir, root, "v1", 12<<10)
	if _, ok := c.get(a); !ok {
		t.Fatal("most recently used entry evicted")
	}
	if _, ok := c.get(b); ok {
		t.Fatal("old entry not evicted")
	}
	c.put(cacheKey{3}, noisyPackage(3, 30000))
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var size int64
	for _, e := range entries {
		info, _ := e.Info()
		size += info.Size()
	}
	if size > 12<<10 {
		t.Fatalf("cache uses %d bytes", size)
	}
	if _, ok := cacheForTest(t, dir, root, "v1", 12<<10).get(a); !ok {
		t.Fatal("oversized entry displaced useful metadata")
	}
}

func TestParseCacheConcurrentWritersLeaveAValidBoundedSnapshot(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		c := cacheForTest(t, dir, root, "v1", 32<<10)
		c.put(cacheKey{byte(i + 1)}, noisyPackage(int64(i), 6000))
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.flush(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	c := cacheForTest(t, dir, root, "v1", 32<<10)
	if len(c.entries) == 0 {
		t.Fatal("all writes were lost")
	}
	entries, _ := os.ReadDir(dir)
	var size int64
	for _, e := range entries {
		info, _ := e.Info()
		size += info.Size()
	}
	if size > 32<<10 {
		t.Fatalf("cache uses %d bytes", size)
	}
}

type cacheTestClient struct {
	pb.JavaParserClient
	calls       atomic.Int32
	fail        bool
	duringParse func()
}

func (c *cacheTestClient) ParsePackage(_ context.Context, in *pb.ParsePackageRequest, _ ...grpc.CallOption) (*pb.Package, error) {
	c.calls.Add(1)
	if c.duringParse != nil {
		c.duringParse()
	}
	if c.fail {
		return nil, errors.New("parse failed")
	}
	return &pb.Package{Name: "example", ImportedClasses: []string{"other.Type"}}, nil
}
func (c *cacheTestClient) ParseJavaPackages(ctx context.Context, in *pb.ParseJavaPackagesRequest, _ ...grpc.CallOption) (*pb.ParseJavaPackagesResponse, error) {
	out := &pb.ParseJavaPackagesResponse{Packages: map[string]*pb.Package{}}
	for _, r := range in.Packages {
		p, e := c.ParsePackage(ctx, r)
		if e != nil {
			return nil, e
		}
		out.Packages[r.Rel] = p
	}
	return out, nil
}

func TestPersistentCacheReusesJavaAndKotlinAcrossWorktrees(t *testing.T) {
	for _, files := range [][]string{{"A.java"}, {"B.kt"}, {"A.java", "B.kt"}} {
		t.Run(files[0], func(t *testing.T) {
			dir := t.TempDir()
			for run := 0; run < 2; run++ {
				root := t.TempDir()
				for _, file := range files {
					writeJavaFile(t, root, "src/"+file)
				}
				client := &cacheTestClient{}
				c := cacheForTest(t, dir, root, "v1", 32<<20)
				runner := &Runner{rpc: client, logger: zerolog.Nop(), cache: c}
				runner.StartPrefetch(root, []SourceRoot{{Rel: "src", Recursive: true}}, 512)
				runner.WaitForPrefetch()
				got, err := runner.parsePackage(context.Background(), &pb.ParsePackageRequest{Rel: "src", Files: files})
				if err != nil || got.Name != "example" {
					t.Fatalf("parse = %v, %v", got, err)
				}
				if run == 0 && client.calls.Load() == 0 {
					t.Fatal("cold run did not parse")
				}
				if run == 1 && client.calls.Load() != 0 {
					t.Fatalf("warm run parsed %d requests", client.calls.Load())
				}
				if err := c.flush(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestParseCacheDoesNotStoreErrorsOrChangingInputs(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "edit during parse"}[change], func(t *testing.T) {
			dir, root := t.TempDir(), t.TempDir()
			writeJavaFile(t, root, "src/A.java")
			client := &cacheTestClient{fail: !change}
			if change {
				client.duringParse = func() {
					if err := os.WriteFile(filepath.Join(root, "src/A.java"), []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			c := cacheForTest(t, dir, root, "v1", 32<<20)
			runner := &Runner{rpc: client, logger: zerolog.Nop(), cache: c}
			request := &pb.ParsePackageRequest{Rel: "src", Files: []string{"A.java"}}
			_, _ = runner.parseOnDemand(context.Background(), request)
			if len(c.entries) != 0 {
				t.Fatal("unstable result was cached")
			}
		})
	}
}

func TestCachedAndUncachedMetadataAgree(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	writeJavaFile(t, root, "src/A.java")
	client := &cacheTestClient{}
	runner := &Runner{rpc: client, logger: zerolog.Nop(), cache: cacheForTest(t, dir, root, "v1", 32<<20)}
	request := &pb.ParsePackageRequest{Rel: "src", Files: []string{"A.java"}}
	a, err := runner.parseOnDemand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	b, err := runner.parseOnDemand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.ImportedClasses, b.ImportedClasses) || client.calls.Load() != 1 {
		t.Fatal("cache changed metadata or reparsed")
	}
}

func TestUnavailableDefaultCacheDoesNotChangeStderr(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("LocalAppData", "")
	var output bytes.Buffer
	runner := &Runner{logger: zerolog.New(&output).Level(zerolog.InfoLevel)}
	runner.EnableCache(t.TempDir(), "", 32<<20)
	if runner.cache != nil || output.Len() != 0 {
		t.Fatalf("unavailable optional cache changed stderr: %s", output.String())
	}
}

func TestParseCacheSharesCompressionAcrossPackages(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	c := cacheForTest(t, dir, root, "v1", 12<<10)
	for i := 0; i < 20; i++ {
		c.put(cacheKey{byte(i + 1)}, noisyPackage(1, 6000))
	}
	if err := c.flush(); err != nil {
		t.Fatal(err)
	}
	c = cacheForTest(t, dir, root, "v1", 12<<10)
	for i := 0; i < 20; i++ {
		if _, ok := c.get(cacheKey{byte(i + 1)}); !ok {
			t.Fatalf("repeated metadata did not fit: missing entry %d", i)
		}
	}
}
