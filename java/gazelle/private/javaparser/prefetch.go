package javaparser

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/bazel-contrib/rules_jvm/java/gazelle/private/javaparser/proto/gazelle/java/javaparser/v0"
	"google.golang.org/grpc"
)

// SourceRoot limits speculative parsing to the directories selected by Gazelle.
type SourceRoot struct {
	Rel       string
	Recursive bool
}

type prefetchedPackage struct {
	files    []string
	metadata *pb.Package
}

type prefetch struct {
	done          chan struct{}
	packages      map[string]prefetchedPackage
	started       time.Time
	lastKeepAlive atomic.Int64
}

// StartPrefetch parses Java packages concurrently, without retaining results between runs.
func (r *Runner) StartPrefetch(repoRoot string, roots []SourceRoot, batchSize int) {
	if batchSize <= 0 || len(roots) == 0 {
		return
	}
	pending := &prefetch{done: make(chan struct{}), packages: make(map[string]prefetchedPackage), started: time.Now()}
	r.prefetch = pending
	go func() {
		defer close(pending.done)
		packages, err := discoverJavaPackages(repoRoot, roots)
		if err != nil {
			r.logger.Debug().Err(err).Msg("skipping Java prefetch")
			return
		}
		jobs := make(chan []*pb.ParsePackageRequest)
		var workers sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < min(4, (len(packages)+batchSize-1)/batchSize); i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for batch := range jobs {
					response, err := r.rpc.ParseJavaPackages(context.Background(),
						&pb.ParseJavaPackagesRequest{Packages: batch}, grpc.MaxCallRecvMsgSize(64<<20))
					if err != nil {
						r.logger.Debug().Err(err).Msg("Java batch will be parsed on demand")
						continue
					}
					mu.Lock()
					for _, request := range batch {
						if metadata := response.GetPackages()[request.Rel]; metadata != nil {
							pending.packages[request.Rel] = prefetchedPackage{files: request.Files, metadata: metadata}
						}
					}
					mu.Unlock()
				}
			}()
		}
		for start := 0; start < len(packages); start += batchSize {
			jobs <- packages[start:min(start+batchSize, len(packages))]
		}
		close(jobs)
		workers.Wait()
	}()
}

func (r *Runner) WaitForPrefetch() {
	if r.prefetch != nil {
		<-r.prefetch.done
	}
}

func (r Runner) parsePackage(ctx context.Context, request *pb.ParsePackageRequest) (*pb.Package, error) {
	if r.prefetch != nil && onlyJavaFiles(request.Files) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.prefetch.done:
		}
		if cached, ok := r.prefetch.packages[request.Rel]; ok {
			files := slices.Clone(request.Files)
			slices.Sort(files)
			// Exclusions and mixed-language packages must use Gazelle's actual selection.
			if slices.Equal(files, cached.files) {
				if err := r.keepParserAlive(ctx); err != nil {
					return nil, err
				}
				return cached.metadata, nil
			}
		}
	}
	return r.rpc.ParsePackage(ctx, request)
}

func (r Runner) keepParserAlive(ctx context.Context) error {
	now := time.Since(r.prefetch.started).Nanoseconds()
	last := r.prefetch.lastKeepAlive.Load()
	if now-last < int64(10*time.Second) || !r.prefetch.lastKeepAlive.CompareAndSwap(last, now) {
		return nil
	}
	// Reusing metadata must not let the parser's idle timeout expire before an uncached request.
	_, err := r.rpc.ParsePackage(ctx, &pb.ParsePackageRequest{})
	return err
}

func onlyJavaFiles(files []string) bool {
	for _, file := range files {
		if !strings.HasSuffix(file, ".java") {
			return false
		}
	}
	return len(files) > 0
}

func discoverJavaPackages(repoRoot string, roots []SourceRoot) ([]*pb.ParsePackageRequest, error) {
	packages := make(map[string]*pb.ParsePackageRequest)
	for _, root := range roots {
		relative := filepath.Clean(root.Rel)
		if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("Java source root %q is outside the repository", root.Rel)
		}
		directory := filepath.Join(repoRoot, relative)
		err := filepath.WalkDir(directory, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == ".git" || strings.HasPrefix(entry.Name(), "bazel-") || (!root.Recursive && file != directory) {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".java") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, filepath.Dir(file))
			if err != nil {
				return err
			}
			if rel == "." {
				rel = ""
			}
			rel = filepath.ToSlash(rel)
			request := packages[rel]
			if request == nil {
				request = &pb.ParsePackageRequest{Rel: rel}
				packages[rel] = request
			}
			request.Files = append(request.Files, entry.Name())
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	requests := make([]*pb.ParsePackageRequest, 0, len(packages))
	for _, request := range packages {
		slices.Sort(request.Files)
		request.Files = slices.Compact(request.Files)
		requests = append(requests, request)
	}
	slices.SortFunc(requests, func(a, b *pb.ParsePackageRequest) int {
		return strings.Compare(a.Rel, b.Rel)
	})
	return requests, nil
}
