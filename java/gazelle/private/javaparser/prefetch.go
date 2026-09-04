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
		for i := 0; i < min(max(1, cap(r.parserSlots)), (len(packages)+batchSize-1)/batchSize); i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for batch := range jobs {
					response, err := r.parseBatch(context.Background(), batch)
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
			// Exclusions must use Gazelle's actual selection.
			if slices.Equal(files, cached.files) {
				if err := r.keepParserAlive(ctx); err != nil {
					return nil, err
				}
				return cached.metadata, nil
			}
		}
	}
	return r.parseMixedPackage(ctx, request)
}

func (r Runner) parseMixedPackage(ctx context.Context, request *pb.ParsePackageRequest) (*pb.Package, error) {
	if r.prefetch == nil {
		return r.parseOnDemand(ctx, request)
	}
	// Mixed packages must not introduce another wait for speculative parsing.
	select {
	case <-r.prefetch.done:
	default:
		return r.parseOnDemand(ctx, request)
	}
	cached, ok := r.prefetch.packages[request.Rel]
	if !ok || cached.metadata.GetName() == "" {
		return r.parseOnDemand(ctx, request)
	}
	var javaFiles, kotlinFiles []string
	for _, file := range request.Files {
		switch {
		case strings.HasSuffix(file, ".java"):
			javaFiles = append(javaFiles, file)
		case strings.HasSuffix(file, ".kt"):
			kotlinFiles = append(kotlinFiles, file)
		default:
			return r.parseOnDemand(ctx, request)
		}
	}
	slices.Sort(javaFiles)
	if len(kotlinFiles) == 0 || !slices.Equal(javaFiles, cached.files) {
		return r.parseOnDemand(ctx, request)
	}
	kotlin, err := r.parseOnDemand(ctx, &pb.ParsePackageRequest{Rel: request.Rel, Files: kotlinFiles})
	if err != nil {
		// Java files can change the combined parser's package-validation error.
		return r.parseOnDemand(ctx, request)
	}
	// An empty wire name cannot distinguish a default package from no package declaration.
	// Let the combined parser retain its validation and error messages in ambiguous cases.
	if kotlin.GetName() != cached.metadata.GetName() {
		return r.parseOnDemand(ctx, request)
	}
	return mergePackageMetadata(cached.metadata, kotlin), nil
}

func mergePackageMetadata(a, b *pb.Package) *pb.Package {
	result := &pb.Package{
		Name:                                   a.Name,
		ImportedClasses:                        unionStrings(a.ImportedClasses, b.ImportedClasses),
		ImportedPackagesWithoutSpecificClasses: unionStrings(a.ImportedPackagesWithoutSpecificClasses, b.ImportedPackagesWithoutSpecificClasses),
		Mains:                                  unionStrings(a.Mains, b.Mains),
		ExportedClasses:                        unionStrings(a.ExportedClasses, b.ExportedClasses),
		InternalClasses:                        unionStrings(a.InternalClasses, b.InternalClasses),
		DeclaredClasses:                        unionStrings(a.DeclaredClasses, b.DeclaredClasses),
		PerClassMetadata:                       make(map[string]*pb.PerClassMetadata),
	}
	for _, source := range []*pb.Package{a, b} {
		for name, metadata := range source.PerClassMetadata {
			target := result.PerClassMetadata[name]
			if target == nil {
				target = &pb.PerClassMetadata{
					PerMethodMetadata: make(map[string]*pb.PerMethodMetadata),
					PerFieldMetadata:  make(map[string]*pb.PerFieldMetadata),
				}
				result.PerClassMetadata[name] = target
			}
			target.AnnotationClassNames = unionStrings(target.AnnotationClassNames, metadata.AnnotationClassNames)
			for method, annotations := range metadata.PerMethodMetadata {
				target.PerMethodMetadata[method] = &pb.PerMethodMetadata{
					AnnotationClassNames: unionStrings(target.PerMethodMetadata[method].GetAnnotationClassNames(), annotations.GetAnnotationClassNames()),
				}
			}
			for field, annotations := range metadata.PerFieldMetadata {
				target.PerFieldMetadata[field] = &pb.PerFieldMetadata{
					AnnotationClassNames: unionStrings(target.PerFieldMetadata[field].GetAnnotationClassNames(), annotations.GetAnnotationClassNames()),
				}
			}
		}
	}
	return result
}

func unionStrings(a, b []string) []string {
	values := slices.Concat(a, b)
	slices.Sort(values)
	return slices.Compact(values)
}

func (r Runner) acquireParser(ctx context.Context) error {
	if r.parserSlots == nil {
		return nil
	}
	select {
	case r.parserSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r Runner) releaseParser() {
	if r.parserSlots != nil {
		<-r.parserSlots
	}
}

func (r Runner) parseUncached(ctx context.Context, request *pb.ParsePackageRequest) (*pb.Package, error) {
	if err := r.acquireParser(ctx); err != nil {
		return nil, err
	}
	defer r.releaseParser()
	return r.rpc.ParsePackage(ctx, request)
}

func (r Runner) parseBatchUncached(ctx context.Context, batch []*pb.ParsePackageRequest) (*pb.ParseJavaPackagesResponse, error) {
	if err := r.acquireParser(ctx); err != nil {
		return nil, err
	}
	defer r.releaseParser()
	started := time.Now()
	defer func() {
		r.logger.Debug().Int("packages", len(batch)).Dur("duration", time.Since(started)).Msg("Java batch parsed")
	}()
	return r.rpc.ParseJavaPackages(ctx, &pb.ParseJavaPackagesRequest{Packages: batch}, grpc.MaxCallRecvMsgSize(64<<20))
}

func (r Runner) keepParserAlive(ctx context.Context) error {
	var started time.Time
	var ping *atomic.Int64
	if r.prefetch != nil {
		started, ping = r.prefetch.started, &r.prefetch.lastKeepAlive
	} else if r.cache != nil {
		started, ping = r.cache.started, &r.cache.lastKeepAlive
	} else {
		return nil
	}
	now := time.Since(started).Nanoseconds()
	last := ping.Load()
	if now-last < int64(10*time.Second) || !ping.CompareAndSwap(last, now) {
		return nil
	}
	// Reusing metadata must not let the parser's idle timeout expire before an uncached request.
	_, err := r.parseOnDemand(ctx, &pb.ParsePackageRequest{})
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
