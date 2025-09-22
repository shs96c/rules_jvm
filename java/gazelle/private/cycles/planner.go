package cycles

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	jpkg "github.com/bazel-contrib/rules_jvm/java/gazelle/private/java"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/sorted_set"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
)

// DirInfo captures parsed information for a directory containing Java sources.
// rel is the path relative to Bazel workspace root.
// files are basenames within that directory.
// pkgName is the declared java package name for those files (one package per dir assumed).
// testPackage indicates whether this is a test package.
// importedPkgs are package-level imports (includes imported classes and on-demand packages),
// exportedClasses are class names exported by this directory's sources.
// mains are classes with main methods in this dir.
// Note: This is a minimal subset of java.Package needed for planning cycles.

type DirInfo struct {
	Rel          string
	Files        []string
	PkgName      types.PackageName
	TestPackage  bool
	ImportedPkgs *sorted_set.SortedSet[types.PackageName]
	ExportedPkgs *sorted_set.SortedSet[types.PackageName]
}

// GroupPlan represents a planned consolidated target for a cycle group.
// LCA is the directory where the combined java_library will be generated.
// Members are the directories that participate in the cycle (including LCA, if applicable).
// Srcs are all Java source files from all members and any sources in the LCA dir,
// expressed as paths relative to the Bazel workspace root.
// Packages is the set of all package names covered by the combined target.
// Note: We intentionally omit deps/exports computation here for initial integration.

type GroupPlan struct {
	LCA      string
	Members  map[string]struct{}
	Srcs     []string
	Packages *sorted_set.SortedSet[types.PackageName]
	Imports  *sorted_set.SortedSet[types.PackageName]
	Exports  *sorted_set.SortedSet[types.PackageName]
}

// Planner plans cycle consolidation decisions lazily.

type Planner struct {
	// Inputs
	RepoRoot string

	// Data
	dirs          map[string]*DirInfo        // rel -> info
	pkgToDir      map[string]string          // package name -> rel
	graph         map[string]map[string]bool // adjacency list: rel -> set of rels
	groups        []*GroupPlan               // finalized groups
	dirToGroupLCA map[string]string          // rel -> group's LCA rel
	lcaToGroup    map[string]*GroupPlan      // LCA -> group plan
}

func NewPlanner(repoRoot string) *Planner {
	return &Planner{
		RepoRoot:      repoRoot,
		dirs:          make(map[string]*DirInfo),
		pkgToDir:      make(map[string]string),
		graph:         make(map[string]map[string]bool),
		groups:        []*GroupPlan{},
		dirToGroupLCA: make(map[string]string),
		lcaToGroup:    make(map[string]*GroupPlan),
	}
}

func (p *Planner) IsSuppressed(rel string) bool {
	lca, ok := p.dirToGroupLCA[rel]
	if !ok {
		return false
	}
	return lca != "" && lca != rel
}

func (p *Planner) IsLCA(rel string) bool {
	_, ok := p.lcaToGroup[rel]
	return ok
}

func (p *Planner) GroupForLCA(rel string) *GroupPlan { return p.lcaToGroup[rel] }

// EnsureCycleDecisionForDir lazily expands the dependency subgraph reachable from rel and
// decides whether rel participates in a cycle, and if so, computes the GroupPlan.
func (p *Planner) EnsureCycleDecisionForDir(ctx context.Context, rel string, parse func(ctx context.Context, rel string, files []string) (*jpkg.Package, error)) error {
	if p.IsSuppressed(rel) || p.IsLCA(rel) {
		return nil
	}
	// Expand reachable subgraph
	visited := make(map[string]bool)
	stack := []string{rel}
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[d] {
			continue
		}
		visited[d] = true
		if err := p.ensureParsedDir(ctx, d, parse); err != nil {
			// best-effort: warn via returning nil; the resolve phase will warn, too.
			continue
		}
		deps := p.resolveImportedPackagesToDirs(d)
		for _, depDir := range deps {
			// Add graph edge
			if p.graph[d] == nil {
				p.graph[d] = make(map[string]bool)
			}
			p.graph[d][depDir] = true
			// Continue exploring
			if !visited[depDir] {
				stack = append(stack, depDir)
			}
		}
	}

	// Build induced subgraph of visited nodes and compute SCCs
	subgraph := make(map[string]map[string]bool)
	for v := range visited {
		if p.graph[v] == nil {
			continue
		}
		for w := range p.graph[v] {
			if !visited[w] {
				continue
			}
			if subgraph[v] == nil {
				subgraph[v] = make(map[string]bool)
			}
			subgraph[v][w] = true
		}
	}
	sccs := stronglyConnectedComponents(subgraph)
	for _, comp := range sccs {
		containsRel := false
		for _, n := range comp {
			if n == rel {
				containsRel = true
				break
			}
		}
		if containsRel && len(comp) >= 2 {
			// Compute LCA and finalize group lazily
			lca := lcaOfDirs(comp)
			g := &GroupPlan{
				LCA:      lca,
				Members:  make(map[string]struct{}),
				Packages: sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
				Imports:  sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
				Exports:  sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
			}
			for _, m := range comp {
				g.Members[m] = struct{}{}
				if info := p.dirs[m]; info != nil {
					if info.PkgName.Name != "" {
						g.Packages.Add(info.PkgName)
					}
					// collect srcs
					for _, f := range info.Files {
						g.Srcs = append(g.Srcs, filepath.ToSlash(filepath.Join(m, f)))
					}
					g.Imports.AddAll(info.ImportedPkgs)
					g.Exports.AddAll(info.ExportedPkgs)
				}
			}
			// include LCA dir sources if any
			if info := p.dirs[lca]; info != nil {
				for _, f := range info.Files {
					g.Srcs = append(g.Srcs, filepath.ToSlash(filepath.Join(lca, f)))
				}
				if info.PkgName.Name != "" {
					g.Packages.Add(info.PkgName)
				}
			}
			sort.Strings(g.Srcs)
			// Filter imports/exports to exclude intra-group packages
			filtered := sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess)
			for _, imp := range g.Imports.SortedSlice() {
				if !g.Packages.Contains(imp) {
					filtered.Add(imp)
				}
			}
			g.Imports = filtered
			filtered = sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess)
			for _, exp := range g.Exports.SortedSlice() {
				if !g.Packages.Contains(exp) {
					filtered.Add(exp)
				}
			}
			g.Exports = filtered

			// Record group and suppress participants
			p.groups = append(p.groups, g)
			p.lcaToGroup[g.LCA] = g
			for m := range g.Members {
				p.dirToGroupLCA[m] = g.LCA
			}
			return nil
		}
	}
	return nil
}

func (p *Planner) ensureParsedDir(ctx context.Context, rel string, parse func(ctx context.Context, rel string, files []string) (*jpkg.Package, error)) error {
	if _, ok := p.dirs[rel]; ok {
		return nil
	}
	files, err := p.javaFilesInDir(rel)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		// still record empty to avoid repeated IO
		p.dirs[rel] = &DirInfo{Rel: rel, Files: nil, PkgName: types.NewPackageName("")}
		return nil
	}
	resp, err := parse(ctx, rel, files)
	if err != nil {
		return err
	}
	di := &DirInfo{
		Rel:          rel,
		Files:        append([]string{}, files...),
		PkgName:      resp.Name,
		TestPackage:  resp.TestPackage,
		ImportedPkgs: sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
		ExportedPkgs: sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
	}
	for _, cls := range resp.ImportedClasses.SortedSlice() {
		di.ImportedPkgs.Add(cls.PackageName())
	}
	di.ImportedPkgs.AddAll(resp.ImportedPackagesWithoutSpecificClasses)
	for _, exp := range resp.ExportedClasses.SortedSlice() {
		di.ExportedPkgs.Add(exp.PackageName())
	}
	p.dirs[rel] = di
	if di.PkgName.Name != "" {
		p.pkgToDir[di.PkgName.Name] = rel
	}
	return nil
}

func (p *Planner) javaFilesInDir(rel string) ([]string, error) {
	abs := filepath.Join(p.RepoRoot, rel)
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".java" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// resolveImportedPackagesToDirs resolves imported packages from dir to directories lazily.
func (p *Planner) resolveImportedPackagesToDirs(dir string) []string {
	info := p.dirs[dir]
	if info == nil {
		return nil
	}
	var out []string
	for _, pkg := range info.ImportedPkgs.SortedSlice() {
		if pkg.Name == "" {
			continue
		}
		if d, ok := p.pkgToDir[pkg.Name]; ok {
			if d != dir {
				out = append(out, d)
			}
			continue
		}
		if d, ok := p.findDirForPackage(pkg.Name); ok {
			p.pkgToDir[pkg.Name] = d
			if d != dir {
				out = append(out, d)
			}
		} else {
			// negative cache to avoid repeated scans
			p.pkgToDir[pkg.Name] = ""
		}
	}
	return out
}

// findDirForPackage scans the repo until it finds a .java file whose package declaration matches pkgName.
// Returns (dir, true) if found, otherwise ("", false).
func (p *Planner) findDirForPackage(pkgName string) (string, bool) {
	var found string
	_ = filepath.WalkDir(p.RepoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") || base == "bazel-bin" || base == "bazel-out" || base == "bazel-testlogs" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".java" {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		buf := make([]byte, 8192)
		n, _ := io.ReadFull(f, buf)
		if n <= 0 {
			return nil
		}
		content := string(buf[:n])
		if idx := strings.Index(content, "package "); idx >= 0 {
			rest := content[idx+len("package "):]
			if semi := strings.Index(rest, ";"); semi >= 0 {
				pkg := strings.TrimSpace(rest[:semi])
				if pkg == pkgName {
					dir := filepath.ToSlash(filepath.Dir(path))
					rel, _ := filepath.Rel(p.RepoRoot, dir)
					found = filepath.ToSlash(rel)
					return io.EOF // stop early
				}
			}
		}
		return nil
	})
	if found != "" {
		return found, true
	}
	return "", false
}

// stronglyConnectedComponents returns SCCs as slices of rel paths.
func stronglyConnectedComponents(graph map[string]map[string]bool) [][]string {
	index := 0
	indices := make(map[string]int)
	lowlink := make(map[string]int)
	var stack []string
	onstack := make(map[string]bool)
	var sccs [][]string

	var visit func(v string)
	visit = func(v string) {
		indices[v] = index
		lowlink[v] = index
		index++
		stack = append(stack, v)
		onstack[v] = true
		for w := range graph[v] {
			if _, seen := indices[w]; !seen {
				visit(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onstack[w] {
				if indices[w] < lowlink[v] {
					lowlink[v] = indices[w]
				}
			}
		}
		if lowlink[v] == indices[v] {
			var comp []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onstack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			sccs = append(sccs, comp)
		}
	}
	for v := range graph {
		if _, seen := indices[v]; !seen {
			visit(v)
		}
	}
	return sccs
}

// lcaOfDirs returns the lowest common ancestor directory (by path prefix) of a list of repo-relative directories.
func lcaOfDirs(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	parts := strings.Split(dirs[0], "/")
	for i := 1; i < len(dirs); i++ {
		p := strings.Split(dirs[i], "/")
		j := 0
		for j < len(parts) && j < len(p) && parts[j] == p[j] {
			j++
		}
		parts = parts[:j]
		if len(parts) == 0 {
			break
		}
	}
	return strings.Join(parts, "/")
}
