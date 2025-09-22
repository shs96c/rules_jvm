package cycles

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/javaparser"
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
}

// Planner builds a one-time plan for cycle consolidation for package mode.

type Planner struct {
	planned bool

	// Inputs
	RepoRoot  string
	Workspace string // empty string represents repo root rel path

	// Data
	dirs          map[string]*DirInfo        // rel -> info
	pkgToDir      map[string]string          // fully qualified package name -> rel
	graph         map[string]map[string]bool // adjacency list: rel -> set of rels
	groups        []*GroupPlan               // final merged group plans
	dirToGroupLCA map[string]string          // rel -> group's LCA rel (for suppressed detection)
	lcaToGroup    map[string]*GroupPlan      // LCA rel -> plan
}

func NewPlanner(repoRoot string) *Planner {
	return &Planner{
		RepoRoot:      repoRoot,
		Workspace:     "",
		dirs:          make(map[string]*DirInfo),
		pkgToDir:      make(map[string]string),
		graph:         make(map[string]map[string]bool),
		dirToGroupLCA: make(map[string]string),
		lcaToGroup:    make(map[string]*GroupPlan),
	}
}

func (p *Planner) IsPlanned() bool { return p.planned }

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

// Plan performs a one-time repository scan and computes cycle groups and their LCAs.
// parse is a function that, given a rel dir and basenames, returns a parsed java package.
func (p *Planner) Plan(ctx context.Context, parse func(ctx context.Context, rel string, files []string) (*javaparser.ParsePackageResponse, error)) error {
	if p.planned {
		return nil
	}

	// 1) Discover candidate directories (with .java files), collect file lists.
	candidates := make(map[string][]string)
	err := filepath.WalkDir(p.RepoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		// Fast prune of hidden and bazel output dirs.
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") || base == "bazel-bin" || base == "bazel-out" || base == "bazel-testlogs" || base == "node_modules" {
			return filepath.SkipDir
		}
		entries, rerr := os.ReadDir(path)
		if rerr != nil {
			return rerr
		}
		var javaFiles []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if filepath.Ext(e.Name()) == ".java" {
				javaFiles = append(javaFiles, e.Name())
			}
		}
		if len(javaFiles) > 0 {
			rel, _ := filepath.Rel(p.RepoRoot, path)
			rel = filepath.ToSlash(rel)
			candidates[rel] = javaFiles
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 2) Parse packages for each candidate dir.
	for rel, files := range candidates {
		resp, err := parse(ctx, rel, files)
		if err != nil {
			// Skip directories with parse errors to keep planning fast and best-effort.
			continue
		}
		di := &DirInfo{
			Rel:          rel,
			Files:        append([]string{}, files...),
			PkgName:      resp.Name,
			TestPackage:  resp.TestPackage,
			ImportedPkgs: sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
			ExportedPkgs: sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
		}
		// Collect imported packages (classes and on-demand imports both reduced to package names)
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
	}

	// 3) Build dir dependency graph
	for rel, di := range p.dirs {
		for _, depPkg := range di.ImportedPkgs.SortedSlice() {
			if targetRel, ok := p.pkgToDir[depPkg.Name]; ok && targetRel != rel {
				if p.graph[rel] == nil {
					p.graph[rel] = make(map[string]bool)
				}
				p.graph[rel][targetRel] = true
			}
		}
	}

	// 4) Compute SCCs
	sccs := stronglyConnectedComponents(p.graph)

	// 5) Build initial groups from SCCs with size >= 2
	var groups []*GroupPlan
	for _, comp := range sccs {
		if len(comp) < 2 {
			continue
		}
		lca := lcaOfDirs(comp)
		g := &GroupPlan{
			LCA:      lca,
			Members:  make(map[string]struct{}),
			Packages: sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
		}
		for _, m := range comp {
			g.Members[m] = struct{}{}
			if info, ok := p.dirs[m]; ok {
				if info.PkgName.Name != "" {
					g.Packages.Add(info.PkgName)
				}
			}
		}
		groups = append(groups, g)
	}

	// 6) Merge overlapping groups until fixed point
	for {
		merged := false
	outer:
		for i := 0; i < len(groups); i++ {
			for j := i + 1; j < len(groups); j++ {
				if groupsOverlap(groups[i], groups[j]) {
					ng := mergeGroups(groups[i], groups[j])
					// replace i with ng, remove j
					groups[i] = ng
					groups = append(groups[:j], groups[j+1:]...)
					merged = true
					break outer
				}
			}
		}
		if !merged {
			break
		}
	}

	// 7) Populate srcs and maps
	for _, g := range groups {
		// include sources from members + LCA itself
		members := make(map[string]struct{}, len(g.Members))
		for m := range g.Members {
			members[m] = struct{}{}
		}
		members[g.LCA] = struct{}{}

		var srcs []string
		for m := range members {
			if info, ok := p.dirs[m]; ok {
				for _, f := range info.Files {
					srcs = append(srcs, filepath.ToSlash(filepath.Join(m, f)))
				}
			}
		}
		sort.Strings(srcs)
		g.Srcs = srcs
	}

	// 8) Index groups for lookup
	p.groups = groups
	for _, g := range groups {
		p.lcaToGroup[g.LCA] = g
		for m := range g.Members {
			p.dirToGroupLCA[m] = g.LCA
		}
	}

	p.planned = true
	return nil
}

// stronglyConnectedComponents returns SCCs as slices of rel paths.
func stronglyConnectedComponents(graph map[string]map[string]bool) [][]string {
	// Tarjan's algorithm
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
		// If v is a root node, pop the stack and output an SCC
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

	// ensure all nodes included
	for v := range graph {
		if _, seen := indices[v]; !seen {
			visit(v)
		}
	}

	return sccs
}

func splitPath(rel string) []string {
	if rel == "" {
		return []string{}
	}
	return strings.Split(rel, "/")
}

func lcaOfDirs(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	parts := splitPath(dirs[0])
	for i := 1; i < len(dirs); i++ {
		p := splitPath(dirs[i])
		// shrink parts to common prefix
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

func isAncestorDir(ancestor, child string) bool {
	if ancestor == child {
		return true
	}
	if ancestor == "" {
		return true
	}
	if !strings.HasPrefix(child, ancestor+"/") {
		return false
	}
	return true
}

func groupsOverlap(a, b *GroupPlan) bool {
	// Overlap if shared members
	for m := range a.Members {
		if _, ok := b.Members[m]; ok {
			return true
		}
	}
	// Or one's LCA inside the other's subtree
	if isAncestorDir(a.LCA, b.LCA) || isAncestorDir(b.LCA, a.LCA) {
		return true
	}
	// Or any member under the other's LCA
	for m := range a.Members {
		if isAncestorDir(b.LCA, m) {
			return true
		}
	}
	for m := range b.Members {
		if isAncestorDir(a.LCA, m) {
			return true
		}
	}
	return false
}

func mergeGroups(a, b *GroupPlan) *GroupPlan {
	ng := &GroupPlan{
		Members:  make(map[string]struct{}),
		Packages: sorted_set.NewSortedSetFn[types.PackageName](nil, types.PackageNameLess),
	}
	for m := range a.Members {
		ng.Members[m] = struct{}{}
	}
	for m := range b.Members {
		ng.Members[m] = struct{}{}
	}
	// recompute LCA across all members' rels
	var rels []string
	for m := range ng.Members {
		rels = append(rels, m)
	}
	sort.Strings(rels)
	ng.LCA = lcaOfDirs(rels)
	for _, p := range a.Packages.SortedSlice() {
		ng.Packages.Add(p)
	}
	for _, p := range b.Packages.SortedSlice() {
		ng.Packages.Add(p)
	}
	return ng
}
