package gazelle

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/java"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/kotlin"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/maven"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/sorted_set"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/buildtools/build"
	lru "github.com/hashicorp/golang-lru"
)

const languageName = "java"

// Resolver satisfies the resolve.Resolver interface. It's the
// language-specific resolver extension.
//
// See resolve.Resolver for more information.
type Resolver struct {
	lang          *javaLang
	internalCache *lru.Cache
	// classIndex is a lazy per-package index, built only for packages with ambiguous
	// resolution (split packages). Maintains prod/test distinction.
	classIndex map[types.PackageName]*packageClassIndex
	// configs provides a map from pkg to config. This allows us to use the config in
	// Embeds.
	configs map[string]*config.Config
}

// packageClassIndex maps class names to their providing labels for a single package.
// Built lazily per-package only when that package has ambiguous resolution.
type packageClassIndex struct {
	// prod maps bare outer class name -> providers (non-testonly rules)
	prod map[string][]label.Label
	// test maps bare outer class name -> providers (testonly rules)
	test map[string][]label.Label
}

func appendLabelOnce(labels []label.Label, candidate label.Label) []label.Label {
	for _, existing := range labels {
		if existing == candidate {
			return labels
		}
	}
	return append(labels, candidate)
}

func NewResolver(lang *javaLang) *Resolver {
	internalCache, err := lru.New(10000)
	if err != nil {
		lang.logger.Fatal().Err(err).Msg("error creating cache")
	}

	return &Resolver{
		lang:          lang,
		internalCache: internalCache,
		classIndex:    make(map[types.PackageName]*packageClassIndex),
		configs:       make(map[string]*config.Config),
	}
}

func (*Resolver) Name() string {
	return languageName
}

func (jr *Resolver) Imports(c *config.Config, r *rule.Rule, f *rule.File) []resolve.ImportSpec {
	log := jr.lang.logger.With().Str("step", "Imports").Str("rel", f.Pkg).Str("rule", r.Name()).Logger()

	// Cache config for use in Embeds, which doesn't receive config in its interface
	jr.configs[f.Pkg] = c

	if !isJvmLibrary(c, r.Kind()) && r.Kind() != "java_test_suite" && r.Kind() != "java_export" {
		return nil
	}

	lbl := label.New("", f.Pkg, r.Name())

	var out []resolve.ImportSpec
	if pkgs := r.PrivateAttr(packagesKey); pkgs != nil {
		for _, pkg := range pkgs.([]types.ResolvableJavaPackage) {
			out = append(out, resolve.ImportSpec{Lang: languageName, Imp: pkg.String()})
		}
	}
	// NOTE: We intentionally do NOT register classes in Gazelle's global RuleIndex.
	// Class-level resolution uses a lazy, per-package index built only when needed
	// (when package-level resolution is ambiguous due to split packages).
	// This keeps the global index small and fast.

	log.Debug().Str("out", fmt.Sprintf("%#v", out)).Str("label", lbl.String()).Msg("return")
	return out
}

func (jr *Resolver) Embeds(r *rule.Rule, from label.Label) []label.Label {
	embedStrings := r.AttrStrings("embed")
	if isJavaProtoLibrary(jr.configs[from.Pkg], r.Kind()) {
		embedStrings = append(embedStrings, r.AttrString("proto"))
	}

	embedLabels := make([]label.Label, 0, len(embedStrings))
	for _, s := range embedStrings {
		l, err := label.Parse(s)
		if err != nil {
			continue
		}
		l = l.Abs(from.Repo, from.Pkg)
		embedLabels = append(embedLabels, l)
	}
	return embedLabels
}

func (jr *Resolver) Resolve(c *config.Config, ix *resolve.RuleIndex, rc *repo.RemoteCache, r *rule.Rule, imports interface{}, from label.Label) {
	resolveInput := imports.(types.ResolveInput)

	packageConfig := c.Exts[languageName].(javaconfig.Configs)[from.Pkg]
	if packageConfig == nil {
		jr.lang.logger.Fatal().Msg("failed retrieving package config")
	}
	isTestRule := packageConfig.IsTestRule(r.Kind())
	if ruleIsTestOnly(r) {
		isTestRule = true
	}

	// If the current library is exported under a `java_export`, it shouldn't be visible for targets outside the java_export.
	if packageConfig.ResolveToJavaExports() && isJavaLibrary(c, r.Kind()) {
		visibility := jr.lang.javaExportIndex.VisibilityForLabel(from)
		if visibility != nil {
			var asStrings []string
			for _, vis := range visibility.SortedSlice() {
				asStrings = append(asStrings, vis.String())
			}
			// The rule attr replacement code is buggy, because while in `rule.SetAttr` we can replace the RHS of the expression, attr.val is always unchanged. I suspect it has to do with pointer magic.
			// Fixed in https://github.com/bazel-contrib/bazel-gazelle/issues/2045
			r.DelAttr("visibility")
			r.SetAttr("visibility", asStrings)
		}
	}

	jr.populateAttr(c, packageConfig, r, "deps", resolveInput.ImportedPackageNames, resolveInput.ImportedClasses, ix, isTestRule, from, resolveInput.PackageNames)
	jr.populateAttr(c, packageConfig, r, "exports", resolveInput.ExportedPackageNames, resolveInput.ExportedClassNames, ix, isTestRule, from, resolveInput.PackageNames)
	if ruleHasKotlinSources(r) {
		jr.addMavenCompileCompanions(c, packageConfig, r, resolveInput, from)
	}
	if isKotlinLibrary(r.Kind()) {
		ensureKotlinExportsAreCompileDeps(c, r, from)
	}

	jr.populateAssociatesAttr(c, ix, resolveInput, r, isTestRule, from)

	jr.populatePluginsAttr(c, ix, resolveInput, packageConfig, from, isTestRule, r)
}

// addMavenCompileCompanions puts the same-package dependency companions of a
// selected Maven artifact on a Kotlin rule's direct compile classpath. It runs
// after normal resolution so directives, workspace targets, and exact class
// ownership still choose the root; the Maven graph can only supplement a root
// that normal resolution actually selected.
func (jr *Resolver) addMavenCompileCompanions(c *config.Config, pc *javaconfig.Config, r *rule.Rule, resolveInput types.ResolveInput, from label.Label) {
	compileResolver, ok := jr.lang.mavenResolver.(maven.CompileResolver)
	if !ok {
		return
	}

	packages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	for _, packageSet := range []*sorted_set.SortedSet[types.PackageName]{
		resolveInput.ImportedPackageNames,
		resolveInput.ExportedPackageNames,
	} {
		if packageSet == nil {
			continue
		}
		for _, pkg := range packageSet.SortedSlice() {
			packages.Add(pkg)
		}
	}
	for _, classSet := range []*sorted_set.SortedSet[types.ClassName]{
		resolveInput.ImportedClasses,
		resolveInput.ExportedClassNames,
	} {
		if classSet == nil {
			continue
		}
		for _, className := range classSet.SortedSlice() {
			packages.Add(className.PackageName())
		}
	}

	deps := sorted_set.NewSortedSetFn([]label.Label{}, sorted_set.LabelLess)
	selected := make([]label.Label, 0, len(r.AttrStrings("deps"))+len(r.AttrStrings("exports")))
	for _, attrName := range []string{"deps", "exports"} {
		for _, raw := range r.AttrStrings(attrName) {
			parsed, err := label.Parse(raw)
			if err != nil {
				panic(fmt.Sprintf("error converting Kotlin %s %q to label: %v", attrName, raw, err))
			}
			selected = append(selected, parsed.Abs(from.Repo, from.Pkg))
			if attrName == "deps" {
				normalized := normalizeLabelPreference(parsed, c.RepoName, from)
				deps.Add(simplifyLabel(c.RepoName, normalized, from))
			}
		}
	}
	if len(selected) == 0 {
		return
	}

	for _, pkg := range packages.SortedSlice() {
		for _, companion := range compileResolver.CompileCompanions(pkg, selected, pc.ExcludedArtifacts(), pc.MavenRepositoryName()) {
			if resolveInput.PackageNames != nil && resolveInput.PackageNames.Contains(pkg) && mavenLabelLooksLikeWorkspaceOwner(companion, from) {
				continue
			}
			deps.Add(simplifyLabel(c.RepoName, companion, from))
		}
	}
	setManagedLabelAttr(r, "deps", deps)
}

// ensureKotlinExportsAreCompileDeps puts every exported library on this target's own
// compile classpath. rules_kotlin propagates exports to consumers but deliberately builds
// a kt_jvm_library's compile classpath from deps and associates only.
//
// This runs before associates normalization so a same-module Kotlin dependency may still
// move from deps to associates, which supplies the same compile edge without listing the
// target in both attributes.
func ensureKotlinExportsAreCompileDeps(c *config.Config, r *rule.Rule, from label.Label) {
	if len(r.AttrStrings("exports")) == 0 {
		return
	}

	compileDeps := sorted_set.NewSortedSetFn([]label.Label{}, sorted_set.LabelLess)
	for _, attrName := range []string{"deps", "exports"} {
		for _, raw := range r.AttrStrings(attrName) {
			parsed, err := label.Parse(raw)
			if err != nil {
				panic(fmt.Sprintf("error converting Kotlin %s %q to label: %v", attrName, raw, err))
			}
			normalized := normalizeLabelPreference(parsed, c.RepoName, from)
			compileDeps.Add(simplifyLabel(c.RepoName, normalized, from))
		}
	}
	setManagedLabelAttr(r, "deps", compileDeps)
}

// populateAssociatesAttr makes a Kotlin test target a friend (associate) of the production
// library for its own package(s). Gradle compiles a module's main and test sources as one
// Kotlin module, so tests can read main's `internal` members; per-package Bazel targets are
// separate modules, and a rules_kotlin associate restores that single-module friendship.
//
// The associate must itself be a single module, so it is the same-package production
// counterpart: the in-repo, non-test provider of the test's own package (found via the index;
// for a collapsed SCC main library this is the one target that registers the package). When a
// package has more than one in-repo provider the friendship is ambiguous, so it is skipped --
// the resulting "internal in another module" compile error points at the real problem.
//
// `associates` exists only on Kotlin test rules, so this is gated on the rule having Kotlin
// sources; a java_test/java_junit5_test has no such attribute.
func (jr *Resolver) populateAssociatesAttr(c *config.Config, ix *resolve.RuleIndex, resolveInput types.ResolveInput, r *rule.Rule, isTestRule bool, from label.Label) {
	if !ruleHasKotlinSources(r) {
		return
	}
	if !isTestRule {
		jr.populateProductionAssociatesAttr(c, r, from)
		return
	}

	associates := sorted_set.NewSortedSetFn([]label.Label{}, sorted_set.LabelLess)
	configs := c.Exts[languageName].(javaconfig.Configs)
	pc := configs[from.Pkg]
	for _, pkg := range resolveInput.PackageNames.SortedSlice() {
		for _, mainLabel := range jr.productionAssociateLabels(c, pc, ix, resolveInput, pkg, from).SortedSlice() {
			if mainLabel == from.Abs(from.Repo, from.Pkg) {
				continue
			}
			// Generated current-repository libraries are recorded with repository-less
			// labels, while Gazelle's rule index returns labels qualified by RepoName.
			if mainLabel.Repo != "" && mainLabel.Repo != c.RepoName {
				continue
			}
			kotlinLibraryKey := label.New("", mainLabel.Pkg, mainLabel.Name).String()
			if !jr.lang.kotlinLibraries[kotlinLibraryKey] {
				continue
			}
			associates.Add(simplifyLabel(c.RepoName, mainLabel, from))
		}
	}
	if associates.Len() == 0 {
		return
	}

	asStrings := make([]string, 0, associates.Len())
	associateSet := make(map[label.Label]struct{}, associates.Len())
	for _, a := range associates.SortedSlice() {
		s := a.String()
		asStrings = append(asStrings, s)
		associateSet[normalizeLabelPreference(a, c.RepoName, from)] = struct{}{}
	}
	replaceStringListAttr(r, "associates", asStrings)

	// An associate is a friend dependency already on the compile and runtime classpath, so
	// drop it from deps to avoid naming the same target twice (rules_kotlin treats associates
	// as deps).
	if deps := r.AttrStrings("deps"); len(deps) > 0 {
		kept := make([]string, 0, len(deps))
		for _, d := range deps {
			parsed, err := label.Parse(d)
			if err != nil {
				kept = append(kept, d)
				continue
			}
			if _, found := associateSet[normalizeLabelPreference(parsed, c.RepoName, from)]; !found {
				kept = append(kept, d)
			}
		}
		replaceStringListAttr(r, "deps", kept)
	}
}

func (jr *Resolver) productionAssociateLabels(c *config.Config, pc *javaconfig.Config, ix *resolve.RuleIndex, resolveInput types.ResolveInput, pkg types.PackageName, from label.Label) *sorted_set.SortedSet[label.Label] {
	labels := sorted_set.NewSortedSetFn([]label.Label{}, sorted_set.LabelLess)
	mainSpec := resolve.ImportSpec{Lang: languageName, Imp: types.NewResolvableJavaPackage(pkg, false, false).String()}
	matches := ix.FindRulesByImportWithConfig(c, mainSpec, languageName)
	if len(matches) == 1 {
		labels.Add(matches[0].Label.Abs(from.Repo, from.Pkg))
		return labels
	}
	if resolveInput.ImportedClasses == nil {
		return labels
	}
	for _, className := range resolveInput.ImportedClasses.SortedSlice() {
		if className.PackageName() != pkg {
			continue
		}
		l := jr.resolveSingleClass(c, pc, className, ix, from, false, nil)
		if l != label.NoLabel {
			labels.Add(l.Abs(from.Repo, from.Pkg))
		}
	}
	return labels
}

// replaceStringListAttr clears the destination AST before setting a managed list.
// Gazelle's rule.SetAttr updates AttrStrings but may leave the old expression in place.
func replaceStringListAttr(r *rule.Rule, attrName string, values []string) {
	r.DelAttr(attrName)
	if len(values) > 0 {
		r.SetAttr(attrName, values)
	}
}

func ruleHasKotlinSources(r *rule.Rule) bool {
	for _, src := range r.AttrStrings("srcs") {
		if strings.HasSuffix(src, ".kt") {
			return true
		}
	}
	return false
}

// ruleIsTestOnly reports whether the rule sets `testonly = True`. Older Gazelle
// releases stored `SetAttr("testonly", true)` as `*build.LiteralExpr{Token:"True"}`;
// current releases store it as `*build.Ident{Name:"True"}`. Accept either shape so
// downstream logic (isTestRule) works across Gazelle versions.
func ruleIsTestOnly(r *rule.Rule) bool {
	switch v := r.Attr("testonly").(type) {
	case *build.Ident:
		return v.Name == "True"
	case *build.LiteralExpr:
		return v.Token == "True"
	}
	return false
}

// resolveKotlinReflectionFunctionClass handles Kotlin reflection function types
// synthesized by the compiler. kotlin-reflect provides these built-ins without
// corresponding class entries for Maven's exact-class index.
func resolveKotlinReflectionFunctionClass(pc *javaconfig.Config, className types.ClassName) label.Label {
	const prefix = "kotlin.reflect.KFunction"
	name := className.FullyQualifiedClassName()
	if !strings.HasPrefix(name, prefix) {
		return label.NoLabel
	}

	functionArity := strings.TrimPrefix(name, prefix)
	if functionArity == "" {
		return label.NoLabel
	}
	for _, digit := range functionArity {
		if digit < '0' || digit > '9' {
			return label.NoLabel
		}
	}

	return maven.LabelFromArtifact(pc.MavenRepositoryName(), "org.jetbrains.kotlin:kotlin-reflect")
}

// resolveMavenWholePackageClass is the last class-level fallback after exact
// Maven ownership and workspace/CrossResolver ownership have both missed.
// It lets compact whole-package Maven index entries participate without
// overriding a class that the current workspace defines.
func (jr *Resolver) resolveMavenWholePackageClass(pc *javaconfig.Config, className types.ClassName) (label.Label, error) {
	if l := resolveKotlinReflectionFunctionClass(pc, className); l != label.NoLabel {
		return l, nil
	}

	excludedArtifacts := pc.ExcludedArtifacts()
	mavenRepositoryName := pc.MavenRepositoryName()
	packageName := className.PackageName()
	l, err := jr.lang.mavenResolver.Resolve(packageName, excludedArtifacts, mavenRepositoryName)
	if err == nil {
		return l, nil
	}
	if !isMavenPackageMissOrAmbiguity(err) {
		return label.NoLabel, err
	}

	seen := map[string]struct{}{packageName.Name: {}}
	parts := strings.Split(className.FullyQualifiedClassName(), ".")
	for i := len(parts) - 1; i > 0; i-- {
		candidateName := strings.Join(parts[:i], ".")
		if _, ok := seen[candidateName]; ok {
			continue
		}
		seen[candidateName] = struct{}{}
		candidatePackage := types.NewPackageName(candidateName)
		l, err = jr.lang.mavenResolver.Resolve(candidatePackage, excludedArtifacts, mavenRepositoryName)
		if err == nil {
			return l, nil
		}
		if !isMavenPackageMissOrAmbiguity(err) {
			return label.NoLabel, err
		}
	}
	return label.NoLabel, nil
}

func isMavenPackageMissOrAmbiguity(err error) bool {
	var noExternal *maven.NoExternalImportsError
	var multipleExternal *maven.MultipleExternalImportsError
	return errors.As(err, &noExternal) || errors.As(err, &multipleExternal)
}

func findClassRuleWithOverride(c *config.Config, className types.ClassName) (label.Label, bool) {
	exactName := className.FullyQualifiedClassName()
	importSpec := resolve.ImportSpec{Lang: languageName, Imp: exactName}
	if ol, found := resolve.FindRuleWithOverride(c, importSpec, languageName); found {
		return ol, true
	}

	outerName := className.FullyQualifiedOuterClassName()
	if outerName == exactName {
		return label.NoLabel, false
	}
	importSpec.Imp = outerName
	return resolve.FindRuleWithOverride(c, importSpec, languageName)
}

func findPackageRuleWithOverride(c *config.Config, packageName types.PackageName) (label.Label, bool) {
	importSpec := resolve.ImportSpec{Lang: languageName, Imp: types.NewResolvableJavaPackage(packageName, false, false).String()}
	return resolve.FindRuleWithOverride(c, importSpec, languageName)
}

func (jr *Resolver) populateAttr(c *config.Config, pc *javaconfig.Config, r *rule.Rule, attrName string, requiredPackageNames *sorted_set.SortedSet[types.PackageName], importedClasses *sorted_set.SortedSet[types.ClassName], ix *resolve.RuleIndex, isTestRule bool, from label.Label, ownPackageNames *sorted_set.SortedSet[types.PackageName]) {
	labels := sorted_set.NewSortedSetFn[label.Label]([]label.Label{}, sorted_set.LabelLess)
	preferredExistingLabels := collectExistingLabelPreferences(r, attrName, c.RepoName, from)

	// Build a map of package -> classes for efficient lookup during class-level resolution
	classesByPackage := make(map[types.PackageName][]types.ClassName)
	if importedClasses != nil {
		for _, cls := range importedClasses.SortedSlice() {
			pkg := cls.PackageName()
			classesByPackage[pkg] = append(classesByPackage[pkg], cls)
		}
	}

	for _, imp := range requiredPackageNames.SortedSlice() {
		// rules_kotlin supplies the Kotlin standard library implicitly, but Java rules do
		// not. Only suppress kotlin.* dependencies for targets that contain Kotlin sources.
		if ruleHasKotlinSources(r) && kotlin.IsStdlib(imp) {
			continue
		}

		var pkgClasses []string
		for _, cls := range classesByPackage[imp] {
			pkgClasses = append(pkgClasses, cls.BareOuterClassName())
		}

		if ol, found := findPackageRuleWithOverride(c, imp); found {
			labels.Add(simplifyLabel(c.RepoName, ol, from))
			continue
		}

		// Check if any imported class has an explicit resolve directive.
		// If so, we must use class-level resolution to respect those overrides,
		// since package-level resolution (including resolve_regexp) would otherwise
		// take precedence and ignore class-specific directives.
		hasClassOverrides := false
		if len(classesByPackage[imp]) > 0 {
			for _, className := range classesByPackage[imp] {
				if _, found := findClassRuleWithOverride(c, className); found {
					hasClassOverrides = true
					break
				}
			}
		}

		// If there are class-level overrides, skip package-level resolution and go
		// directly to class-level resolution to ensure overrides are respected.
		if hasClassOverrides {
			jr.lang.logger.Debug().
				Str("package", imp.Name).
				Strs("classes", pkgClasses).
				Stringer("from", from).
				Msg("class-level resolve directive found, using class-level resolution")

			for _, className := range classesByPackage[imp] {
				// Check for explicit resolve directive for this specific class first
				if ol, found := findClassRuleWithOverride(c, className); found {
					labels.Add(simplifyLabel(c.RepoName, ol, from))
					continue
				}

				l, err := jr.lang.mavenResolver.ResolveClass(className, pc.ExcludedArtifacts(), pc.MavenRepositoryName())
				if err != nil {
					jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
					// A split Maven package may be ambiguous even when a workspace or
					// generated-code resolver has an exact owner for this class.
					l = label.NoLabel
				}
				if l == label.NoLabel {
					l = jr.resolveSingleClass(c, pc, className, ix, from, isTestRule, preferredExistingLabels)
				}
				if l == label.NoLabel {
					l, err = jr.resolveMavenWholePackageClass(pc, className)
					if err != nil {
						jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
					}
				}
				if l != label.NoLabel {
					labels.Add(simplifyLabel(c.RepoName, l, from))
				}
			}
			continue
		}

		// Try package-level resolution first (fast path)
		dep, ambiguous := jr.resolveSinglePackageWithAmbiguity(c, pc, imp, ix, from, isTestRule, ownPackageNames, pkgClasses, ruleHasKotlinSources(r))
		if dep != label.NoLabel {
			resolvedPackageDep := simplifyLabel(c.RepoName, dep, from)
			if len(classesByPackage[imp]) == 0 {
				labels.Add(resolvedPackageDep)
				continue
			}

			// The package resolved unambiguously to a single target, but an external
			// gazelle plugin or Maven artifact may own some classes of the same package.
			// Resolve each imported class independently when the package target does not
			// declare it. This keeps a workspace helper that owns one class from claiming
			// every Maven class in the enclosing package.
			for _, className := range classesByPackage[imp] {
				if jr.ruleDeclaresClass(dep, className) {
					labels.Add(resolvedPackageDep)
					continue
				}

				l, err := jr.lang.mavenResolver.ResolveClass(className, pc.ExcludedArtifacts(), pc.MavenRepositoryName())
				if err != nil {
					jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
					l = label.NoLabel
				}
				if l != label.NoLabel {
					if !mavenLabelLooksLikeWorkspaceOwner(l, dep) {
						labels.Add(simplifyLabel(c.RepoName, l, from))
						continue
					}
					l = label.NoLabel
				}
				if l := jr.resolveClassFromCrossResolver(c, pc, className, ix, from); l != label.NoLabel {
					labels.Add(l)
					continue
				}
				if isTestRule {
					// A test may import a class from a testonly library (e.g. a testFixtures source
					// set) whose package the resolved production target also owns; the in-repo class
					// index includes testonly providers for test rules.
					if l := jr.resolveSingleClass(c, pc, className, ix, from, true, preferredExistingLabels); l != label.NoLabel {
						labels.Add(l)
						continue
					}
					// Or the class may be a helper in another package's java_test_suite (its
					// "<suite>-test-lib"). Depend on that helper library, but only when it actually
					// declares the class -- a class the production provider doesn't declare may be a
					// main top-level function, not a test helper, which must not pull the lib in.
					if l := jr.resolveTestSuiteHelperClass(c, imp, className, ix, from); l != label.NoLabel {
						labels.Add(l)
						continue
					}
				}

				if jr.ruleDeclaresAnyClassInPackage(dep, className.PackageName()) {
					l, err = jr.resolveMavenWholePackageClass(pc, className)
					if err != nil {
						jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
					}
					if l != label.NoLabel && !mavenLabelLooksLikeWorkspaceOwner(l, dep) {
						labels.Add(simplifyLabel(c.RepoName, l, from))
						continue
					}
				}
				labels.Add(resolvedPackageDep)
			}
			continue
		}

		// Only fall back to class-level resolution when package resolution is ambiguous
		if ambiguous && len(classesByPackage[imp]) > 0 {
			jr.lang.logger.Debug().
				Str("package", imp.Name).
				Strs("classes", pkgClasses).
				Stringer("from", from).
				Msg("package has multiple providers, attempting class-level resolution")

			resolvedAny := false
			for _, className := range classesByPackage[imp] {
				// Check for explicit resolve directive for this specific class first
				if ol, found := findClassRuleWithOverride(c, className); found {
					labels.Add(simplifyLabel(c.RepoName, ol, from))
					resolvedAny = true
					continue
				}

				l, err := jr.lang.mavenResolver.ResolveClass(className, pc.ExcludedArtifacts(), pc.MavenRepositoryName())
				if err != nil {
					jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
					// Do not let Maven ambiguity mask an exact workspace or
					// generated-code provider registered through CrossResolve.
					l = label.NoLabel
				}
				if l == label.NoLabel {
					l = jr.resolveSingleClass(c, pc, className, ix, from, isTestRule, preferredExistingLabels)
				}
				if l == label.NoLabel && isTestRule {
					l = jr.resolveTestSuiteHelperClass(c, imp, className, ix, from)
				}
				if l == label.NoLabel {
					l, err = jr.resolveMavenWholePackageClass(pc, className)
					if err != nil {
						jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
					}
				}
				if l != label.NoLabel {
					labels.Add(simplifyLabel(c.RepoName, l, from))
					resolvedAny = true
				}
			}

			if !resolvedAny {
				jr.lang.logger.Error().
					Str("package", imp.Name).
					Strs("classes", pkgClasses).
					Stringer("from", from).
					Msg("package has multiple providers and class-level resolution failed for all classes")
				jr.lang.hasHadErrors = true
			}
		}
	}

	// A class whose package this rule owns is normally assumed to be provided by the rule
	// itself, so that package is filtered out of requiredPackageNames and the loop above
	// never visits it. The generate step keeps undeclared classes in importedClasses so
	// split-package owners from directives, Maven, or another Gazelle plugin can still be
	// selected here.
	if importedClasses != nil && ownPackageNames != nil {
		for _, className := range importedClasses.SortedSlice() {
			if !ownPackageNames.Contains(className.PackageName()) {
				continue
			}
			if l, found := findClassRuleWithOverride(c, className); found {
				labels.Add(simplifyLabel(c.RepoName, l, from))
				continue
			}
			if l := jr.resolveSingleClass(c, pc, className, ix, from, isTestRule, preferredExistingLabels); l != label.NoLabel {
				labels.Add(simplifyLabel(c.RepoName, l, from))
				continue
			}

			l, err := jr.lang.mavenResolver.ResolveClass(className, pc.ExcludedArtifacts(), pc.MavenRepositoryName())
			if err != nil {
				jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
				l = label.NoLabel
			}
			if l != label.NoLabel {
				if !mavenLabelLooksLikeWorkspaceOwner(l, from) {
					labels.Add(simplifyLabel(c.RepoName, l, from))
					continue
				}
				l = label.NoLabel
			}
			if l := jr.resolveClassFromCrossResolver(c, pc, className, ix, from); l != label.NoLabel {
				labels.Add(l)
				continue
			}

			l, err = jr.resolveMavenWholePackageClass(pc, className)
			if err != nil {
				jr.lang.logger.Warn().Err(err).Str("class", className.FullyQualifiedClassName()).Msg("error resolving class")
			}
			if l != label.NoLabel && !mavenLabelLooksLikeWorkspaceOwner(l, from) {
				labels.Add(simplifyLabel(c.RepoName, l, from))
			}
		}
	}

	setLabelAttrIncludingExistingValues(r, attrName, labels)

}

func (jr *Resolver) populatePluginsAttr(c *config.Config, ix *resolve.RuleIndex, resolveInput types.ResolveInput, packageConfig *javaconfig.Config, from label.Label, isTestRule bool, r *rule.Rule) {
	pluginLabels := sorted_set.NewSortedSetFn[label.Label]([]label.Label{}, labelLess)
	for _, annotationProcessor := range resolveInput.AnnotationProcessors.SortedSlice() {
		dep := jr.resolveSinglePackage(c, packageConfig, annotationProcessor.PackageName(), ix, from, isTestRule, resolveInput.PackageNames, []string{annotationProcessor.BareOuterClassName()})
		if dep == label.NoLabel {
			continue
		}

		// Use the naming scheme for plugins as per https://github.com/bazelbuild/rules_jvm_external/pull/1102
		// In the case of overrides (i.e. # gazelle:resolve targets) we require that they follow the same name-mangling scheme for the java_plugin target as rules_jvm_external uses.
		// Ideally this would be a call to `java_plugin_artifact(dep.String(), annotationProcessor.FullyQualifiedClassName())` but we don't have function calls working in attributes.
		dep.Name += "__java_plugin__" + strings.NewReplacer(".", "_", "$", "_").Replace(annotationProcessor.FullyQualifiedClassName())

		pluginLabels.Add(simplifyLabel(c.RepoName, dep, from))
	}

	setLabelAttrIncludingExistingValues(r, "plugins", pluginLabels)
}

func labelLess(l, r label.Label) bool {
	// In UTF-8, / sorts before :
	// We want relative labels to come before absolute ones, so explicitly sort relative before absolute.
	if l.Relative {
		if r.Relative {
			return l.String() < r.String()
		}
		return true
	}
	if r.Relative {
		return false
	}
	return l.String() < r.String()
}

func simplifyLabel(repoName string, l label.Label, from label.Label) label.Label {
	if l.Repo == repoName || l.Repo == "" {
		if l.Pkg == from.Pkg {
			l.Relative = true
		} else {
			l.Repo = ""
		}
	}
	return l
}

func mavenLabelLooksLikeWorkspaceOwner(mavenLabel label.Label, owner label.Label) bool {
	if mavenLabel.Repo != "maven" {
		return false
	}
	ownerName := owner.Name
	if ownerName == "" {
		ownerName = path.Base(owner.Pkg)
	}
	if ownerName == "" {
		return false
	}
	normalisedOwnerName := strings.ReplaceAll(ownerName, "-", "_")
	if mavenLabel.Name == normalisedOwnerName || strings.HasSuffix(mavenLabel.Name, "_"+normalisedOwnerName) {
		return true
	}
	for _, segment := range strings.Split(owner.Pkg, "/") {
		normalisedSegment := strings.ReplaceAll(segment, "-", "_")
		if normalisedSegment == "" {
			continue
		}
		if mavenLabel.Name == normalisedSegment || strings.HasSuffix(mavenLabel.Name, "_"+normalisedSegment) {
			return true
		}
	}
	return sharedWorkspaceTokenCount(mavenLabel.Name, owner.Pkg) >= 2 && mavenLabelContainsOwnerLeaf(mavenLabel.Name, owner.Pkg)
}

func sharedWorkspaceTokenCount(mavenName, ownerPkg string) int {
	mavenTokens := make(map[string]struct{})
	for _, token := range strings.Split(mavenName, "_") {
		if token == "" || isGenericWorkspaceToken(token) {
			continue
		}
		mavenTokens[token] = struct{}{}
	}
	seen := make(map[string]struct{})
	for _, segment := range strings.Split(ownerPkg, "/") {
		token := strings.ReplaceAll(segment, "-", "_")
		if token == "" || isGenericWorkspaceToken(token) {
			continue
		}
		if _, ok := mavenTokens[token]; ok {
			seen[token] = struct{}{}
		}
	}
	return len(seen)
}

func mavenLabelContainsOwnerLeaf(mavenName, ownerPkg string) bool {
	mavenTokens := make(map[string]struct{})
	for _, token := range strings.Split(mavenName, "_") {
		if token == "" || isGenericWorkspaceToken(token) {
			continue
		}
		mavenTokens[token] = struct{}{}
	}
	segments := strings.Split(ownerPkg, "/")
	for i := len(segments) - 1; i >= 0; i-- {
		token := strings.ReplaceAll(segments[i], "-", "_")
		if token == "" || isGenericWorkspaceToken(token) {
			continue
		}
		_, ok := mavenTokens[token]
		return ok
	}
	return false
}

func isGenericWorkspaceToken(token string) bool {
	switch token {
	case "com", "org", "net", "java", "kotlin", "jvm", "main", "src", "test":
		return true
	default:
		return false
	}
}

// normalizeLabelPreference gives relative, absolute-current-repository, and
// explicit-current-repository spellings the same identity for comparison.
func normalizeLabelPreference(l label.Label, repoName string, from label.Label) label.Label {
	l = l.Abs(from.Repo, from.Pkg)
	if l.Repo == repoName || l.Repo == from.Repo {
		l.Repo = ""
	}
	l.Relative = false
	l.Canonical = false
	return l
}

// collectExistingLabelPreferences snapshots a managed attribute before resolution
// replaces it. Existing edges are only tie-breakers between otherwise ambiguous
// exact-class workspace providers; they are not copied into the generated attribute.
func collectExistingLabelPreferences(r *rule.Rule, attrName, repoName string, from label.Label) map[label.Label]struct{} {
	preferences := make(map[label.Label]struct{})
	for _, raw := range r.AttrStrings(attrName) {
		l, err := label.Parse(raw)
		if err != nil {
			continue
		}
		preferences[normalizeLabelPreference(l, repoName, from)] = struct{}{}
	}
	return preferences
}

// selfClassCandidate returns the candidate owned by the rule being resolved.
// Local ownership takes precedence over preferences for another provider.
func selfClassCandidate(candidates []label.Label, repoName string, from label.Label) label.Label {
	normalizedFrom := normalizeLabelPreference(from, repoName, from)
	for _, candidate := range candidates {
		if normalizeLabelPreference(candidate, repoName, from) == normalizedFrom {
			return candidate
		}
	}
	return label.NoLabel
}

func preferredExistingClassCandidate(candidates []label.Label, preferences map[label.Label]struct{}, repoName string, from label.Label) label.Label {
	matches := make(map[label.Label]label.Label)
	for _, candidate := range candidates {
		normalized := normalizeLabelPreference(candidate, repoName, from)
		if _, preferred := preferences[normalized]; preferred {
			matches[normalized] = candidate
		}
	}
	if len(matches) != 1 {
		return label.NoLabel
	}
	for _, candidate := range matches {
		return candidate
	}
	return label.NoLabel
}

// setLabelAttrIncludingExistingValues is reserved for hand-owned attributes such as plugins.
func setLabelAttrIncludingExistingValues(r *rule.Rule, attrName string, labels *sorted_set.SortedSet[label.Label]) {
	for _, implicitDep := range r.AttrStrings(attrName) {
		l, err := label.Parse(implicitDep)
		if err != nil {
			panic(fmt.Sprintf("error converting implicit %s %q to label: %v", attrName, implicitDep, err))
		}
		labels.Add(l)
	}
	setManagedLabelAttr(r, attrName, labels)
}

// setManagedLabelAttr replaces a Gazelle-managed label list with exactly the
// inferred labels. Gazelle's merge phase preserves values marked with keep.
func setManagedLabelAttr(r *rule.Rule, attrName string, labels *sorted_set.SortedSet[label.Label]) {
	var exprs []build.Expr
	if labels.Len() > 0 {
		for _, l := range labels.SortedSlice() {
			if l.Relative && l.Name == r.Name() {
				continue
			}
			exprs = append(exprs, &build.StringExpr{Value: l.String()})
		}
	}
	r.DelAttr(attrName)
	if len(exprs) > 0 {
		r.SetAttr(attrName, exprs)
	}
}

// resolveSinglePackageWithAmbiguity resolves a package import and returns whether there was ambiguity.
// When ambiguous is true and out is NoLabel, the caller should attempt class-level resolution.
func (jr *Resolver) resolveSinglePackageWithAmbiguity(c *config.Config, pc *javaconfig.Config, imp types.PackageName, ix *resolve.RuleIndex, from label.Label, isTestRule bool, ownPackageNames *sorted_set.SortedSet[types.PackageName], pkgClasses []string, allowMavenCompileGroup bool) (out label.Label, ambiguous bool) {
	cacheKey := types.NewResolvableJavaPackage(imp, false, false)
	importSpec := resolve.ImportSpec{Lang: languageName, Imp: cacheKey.String()}
	if ol, found := resolve.FindRuleWithOverride(c, importSpec, languageName); found {
		return ol, false
	}

	matches := ix.FindRulesByImportWithConfig(c, importSpec, languageName)

	if pc.ResolveToJavaExports() {
		matches = jr.tryResolvingToJavaExport(matches, from)
	} else {
		nonExportMatches := make([]resolve.FindResult, 0)
		for _, match := range matches {
			if !jr.lang.javaExportIndex.IsJavaExport(match.Label) {
				nonExportMatches = append(nonExportMatches, match)
			}
		}
		matches = nonExportMatches
	}

	if len(matches) == 1 {
		return matches[0].Label, false
	}

	if len(matches) > 1 {
		// Multiple matches found - signal ambiguity so caller can try class-level resolution
		return label.NoLabel, true
	}

	if v, ok := jr.internalCache.Get(cacheKey); ok {
		return simplifyLabel(c.RepoName, v.(label.Label), from), false
	}

	jr.lang.logger.Debug().Str("parsedImport", imp.Name).Stringer("from", from).Msg("not found yet")

	cacheResult := true
	defer func() {
		if cacheResult && out != label.NoLabel {
			jr.internalCache.Add(cacheKey, out)
		}
	}()

	if java.IsStdlib(imp) {
		return label.NoLabel, false
	}

	// As per https://github.com/bazelbuild/bazel/blob/347407a88fd480fc5e0fbd42cc8196e4356a690b/tools/java/runfiles/Runfiles.java#L41
	if imp.Name == "com.google.devtools.build.runfiles" {
		runfilesLabel := "@bazel_tools//tools/java/runfiles"
		l, err := label.Parse(runfilesLabel)
		if err != nil {
			jr.lang.logger.Fatal().Str("label", runfilesLabel).Err(err).Msg("failed to parse known-good runfiles label")
			return label.NoLabel, false
		}
		return l, false
	}

	if l, err := jr.lang.mavenResolver.Resolve(imp, pc.ExcludedArtifacts(), pc.MavenRepositoryName()); err != nil {
		var noExternal *maven.NoExternalImportsError
		var multipleExternal *maven.MultipleExternalImportsError

		if errors.As(err, &noExternal) {
			// do not fail, the package might be provided elsewhere
		} else if errors.As(err, &multipleExternal) {
			if allowMavenCompileGroup && len(pkgClasses) == 0 {
				if compileResolver, ok := jr.lang.mavenResolver.(maven.CompileResolver); ok {
					group, compileErr := compileResolver.ResolveCompilePackage(imp, pc.ExcludedArtifacts(), pc.MavenRepositoryName())
					if compileErr == nil && len(group) > 0 {
						// This mode is Kotlin-specific, so do not let its root leak
						// through the package cache to a later Java-only rule.
						cacheResult = false
						return group[0], false
					}
				}
			}
			// Maven has multiple options (split package) - check if class-level resolution is available
			if len(pkgClasses) > 0 {
				// Only signal ambiguity if we have class index data for at least one class
				for _, className := range pkgClasses {
					cls := types.NewClassName(imp, className)
					if resolved, _ := jr.lang.mavenResolver.ResolveClass(cls, pc.ExcludedArtifacts(), pc.MavenRepositoryName()); resolved != label.NoLabel {
						return label.NoLabel, true
					}
				}
			}
			// No class-level resolution available, show helpful error with resolution hints
			jr.lang.logger.Error().Strs("classes", pkgClasses).Msg("Append one of the following to BUILD.bazel:")
			for _, possible := range multipleExternal.PossiblePackages {
				jr.lang.logger.Error().Msgf("# gazelle:resolve java %s %s", imp.Name, possible)
			}
			// Don't return here - let execution continue to produce the warning about unresolved package
		} else {
			jr.lang.logger.Fatal().Err(err).Msg("maven resolver error")
		}
	} else {
		if ownPackageNames != nil && ownPackageNames.Contains(imp) && mavenLabelLooksLikeWorkspaceOwner(l, from) {
			return label.NoLabel, false
		}
		return l, false
	}

	if isTestRule {
		// If there's exactly one testonly match, use it
		testonlyCacheKey := types.NewResolvableJavaPackage(imp, true, false)
		testonlyImportSpec := resolve.ImportSpec{Lang: languageName, Imp: testonlyCacheKey.String()}
		testonlyMatches := ix.FindRulesByImportWithConfig(c, testonlyImportSpec, languageName)
		if len(testonlyMatches) == 1 {
			cacheKey = testonlyCacheKey
			return simplifyLabel(c.RepoName, testonlyMatches[0].Label, from), false
		}

		// If there's exactly one testsuite match, use it
		testsuiteCacheKey := types.NewResolvableJavaPackage(imp, true, true)
		testsuiteImportSpec := resolve.ImportSpec{Lang: languageName, Imp: testsuiteCacheKey.String()}
		testsuiteMatches := ix.FindRulesByImportWithConfig(c, testsuiteImportSpec, languageName)
		if len(testsuiteMatches) == 1 {
			cacheKey = testsuiteCacheKey
			l := testsuiteMatches[0].Label
			if l != from {
				l.Name += "-test-lib"
				return simplifyLabel(c.RepoName, l, from), false
			}
		}
	}

	if isTestRule && ownPackageNames.Contains(imp) {
		// Tests may have unique packages which don't exist outside of those tests - don't treat this as an error.
		return label.NoLabel, false
	}

	// No package-level provider exists, but generated-code extensions commonly
	// expose only exact classes through CrossResolve. Signal the caller to take
	// its class-level path before treating the import as unresolved.
	if len(pkgClasses) > 0 {
		return label.NoLabel, true
	}

	jr.lang.logger.Error().
		Str("package", imp.Name).
		Str("from rule", from.String()).
		Strs("classes", pkgClasses).
		Msg("Unable to find package for import in any dependency")
	jr.lang.hasHadErrors = true

	return label.NoLabel, false
}

func (jr *Resolver) resolveSinglePackage(c *config.Config, pc *javaconfig.Config, imp types.PackageName, ix *resolve.RuleIndex, from label.Label, isTestRule bool, ownPackageNames *sorted_set.SortedSet[types.PackageName], pkgClasses []string) (out label.Label) {
	out, _ = jr.resolveSinglePackageWithAmbiguity(c, pc, imp, ix, from, isTestRule, ownPackageNames, pkgClasses, false)
	return out
}

// buildPackageClassIndex lazily builds a class index for a specific package.
// Only called when package-level resolution is ambiguous (split packages).
func (jr *Resolver) buildPackageClassIndex(c *config.Config, pkg types.PackageName, ix *resolve.RuleIndex) *packageClassIndex {
	if pci, ok := jr.classIndex[pkg]; ok {
		return pci
	}

	// Find all rules that provide this package
	cacheKey := types.NewResolvableJavaPackage(pkg, false, false)
	importSpec := resolve.ImportSpec{Lang: languageName, Imp: cacheKey.String()}
	matches := ix.FindRulesByImportWithConfig(c, importSpec, languageName)

	// Also check for testonly providers
	testCacheKey := types.NewResolvableJavaPackage(pkg, true, false)
	testImportSpec := resolve.ImportSpec{Lang: languageName, Imp: testCacheKey.String()}
	testMatches := ix.FindRulesByImportWithConfig(c, testImportSpec, languageName)
	matches = append(matches, testMatches...)

	// java_test_suite registers helper-bearing packages under a distinct suite
	// import key, while its declared helper classes are cached under the
	// synthetic "<suite>-test-lib" target emitted by the macro.
	testsuiteCacheKey := types.NewResolvableJavaPackage(pkg, true, true)
	testsuiteImportSpec := resolve.ImportSpec{Lang: languageName, Imp: testsuiteCacheKey.String()}
	testsuiteMatches := ix.FindRulesByImportWithConfig(c, testsuiteImportSpec, languageName)

	pci := &packageClassIndex{
		prod: make(map[string][]label.Label),
		test: make(map[string][]label.Label),
	}

	for _, m := range matches {
		// Try lookup without repo prefix since that's how we store entries
		cacheLabel := label.New("", m.Label.Pkg, m.Label.Name)
		info, ok := jr.lang.classExportCache[cacheLabel.String()]
		if !ok {
			continue
		}
		for _, cls := range info.classes {
			if cls.PackageName() != pkg {
				continue
			}
			name := cls.BareOuterClassName()
			if info.testonly {
				pci.test[name] = appendLabelOnce(pci.test[name], m.Label)
			} else {
				pci.prod[name] = appendLabelOnce(pci.prod[name], m.Label)
			}
		}
	}

	for _, m := range testsuiteMatches {
		helperLabel := m.Label
		helperLabel.Name = testHelperLibname(helperLabel.Name)
		cacheLabel := label.New("", helperLabel.Pkg, helperLabel.Name)
		info, ok := jr.lang.classExportCache[cacheLabel.String()]
		if !ok {
			continue
		}
		for _, cls := range info.classes {
			if cls.PackageName() != pkg {
				continue
			}
			name := cls.BareOuterClassName()
			pci.test[name] = appendLabelOnce(pci.test[name], helperLabel)
		}
	}

	jr.classIndex[pkg] = pci
	jr.lang.logger.Debug().
		Str("package", pkg.Name).
		Int("prod_classes", len(pci.prod)).
		Int("test_classes", len(pci.test)).
		Msg("built class index for split package")

	return pci
}

func (jr *Resolver) resolveSingleClass(c *config.Config, pc *javaconfig.Config, className types.ClassName, ix *resolve.RuleIndex, from label.Label, isTestRule bool, preferredExistingLabels map[label.Label]struct{}) (out label.Label) {
	imp := className.FullyQualifiedClassName()
	// Check for manual override first
	if ol, found := findClassRuleWithOverride(c, className); found {
		return ol
	}

	// Build/get the per-package class index
	pkg := className.PackageName()
	pci := jr.buildPackageClassIndex(c, pkg, ix)
	bareClassName := className.BareOuterClassName()

	// Look up candidates - prefer prod classes, but test rules can also use test classes
	var candidates []label.Label
	if prodCandidates, ok := pci.prod[bareClassName]; ok {
		candidates = prodCandidates
	}
	if isTestRule {
		if testCandidates, ok := pci.test[bareClassName]; ok {
			candidates = append(candidates, testCandidates...)
		}
	}

	if len(candidates) == 0 {
		// No in-repo provider for this class. Mirror Gazelle's index-then-CrossResolve
		// ordering at class granularity: consult external plugins via the cross-resolver.
		return jr.resolveClassFromCrossResolver(c, pc, className, ix, from)
	}

	if len(candidates) == 1 {
		return simplifyLabel(c.RepoName, candidates[0], from)
	}

	if self := selfClassCandidate(candidates, c.RepoName, from); self != label.NoLabel {
		return simplifyLabel(c.RepoName, self, from)
	}

	// Multiple candidates - try java_export narrowing
	if pc.ResolveToJavaExports() {
		results := make([]resolve.FindResult, 0, len(candidates))
		for _, l := range candidates {
			results = append(results, resolve.FindResult{Label: l})
		}
		narrowed := jr.tryResolvingToJavaExport(results, from)
		if len(narrowed) == 1 {
			return simplifyLabel(c.RepoName, narrowed[0].Label, from)
		}
	}

	if preferred := preferredExistingClassCandidate(candidates, preferredExistingLabels, c.RepoName, from); preferred != label.NoLabel {
		return simplifyLabel(c.RepoName, preferred, from)
	}

	// Still ambiguous - log error
	labels := make([]string, 0, len(candidates))
	for _, l := range candidates {
		labels = append(labels, l.String())
	}
	sort.Strings(labels)

	jr.lang.logger.Error().
		Str("class", imp).
		Strs("targets", labels).
		Stringer("from", from).
		Msg("resolveSingleClass found MULTIPLE providers for class")

	return label.NoLabel
}

// ruleDeclaresClass reports whether the rule at lbl is known (via the class export
// cache) to provide the outer class of className. It lets the fast path skip
// cross-resolver probes for classes the resolved in-repo target already owns.
func (jr *Resolver) ruleDeclaresClass(lbl label.Label, className types.ClassName) bool {
	cacheLabel := label.New("", lbl.Pkg, lbl.Name)
	info, ok := jr.lang.classExportCache[cacheLabel.String()]
	if !ok {
		return false
	}
	for _, cls := range info.classes {
		if cls.PackageName() == className.PackageName() && cls.BareOuterClassName() == className.BareOuterClassName() {
			return true
		}
	}
	return false
}

func (jr *Resolver) ruleDeclaresAnyClassInPackage(lbl label.Label, packageName types.PackageName) bool {
	cacheLabel := label.New("", lbl.Pkg, lbl.Name)
	info, ok := jr.lang.classExportCache[cacheLabel.String()]
	if !ok {
		return false
	}
	for _, cls := range info.classes {
		if cls.PackageName() == packageName {
			return true
		}
	}
	return false
}

// resolveTestSuiteHelperClass returns the unique "<suite>-test-lib" helper library of a
// java_test_suite that provides imp AND declares className, or NoLabel otherwise.
// java_test_suite moves its non-test sources into a helper library named <suite>-test-lib;
// a test in another package that imports one of those helpers must depend on it. This mirrors
// resolveSinglePackageWithAmbiguity's test-suite branch, which only fires when imp has no
// production provider -- here we cover the case where it has one (a split main/test-suite
// package), so the package itself resolves to production and only the helper class is missing.
// The className check guards against false positives: a class the production provider does not
// declare may be a main top-level function (not registered as a class), which the helper lib
// does not declare either and must not pull in.
func (jr *Resolver) resolveTestSuiteHelperClass(c *config.Config, imp types.PackageName, className types.ClassName, ix *resolve.RuleIndex, from label.Label) label.Label {
	spec := resolve.ImportSpec{Lang: languageName, Imp: types.NewResolvableJavaPackage(imp, true, true).String()}
	matches := ix.FindRulesByImportWithConfig(c, spec, languageName)
	helper := label.NoLabel
	for _, match := range matches {
		candidate := match.Label
		if candidate == from {
			continue
		}
		candidate.Name += "-test-lib"
		if !jr.ruleDeclaresClass(candidate, className) {
			continue
		}
		if helper != label.NoLabel {
			return label.NoLabel
		}
		helper = candidate
	}
	if helper == label.NoLabel {
		return label.NoLabel
	}
	return simplifyLabel(c.RepoName, helper, from)
}

// resolveClassFromCrossResolver consults registered Gazelle CrossResolvers for a
// class-level java import. Because the Java plugin intentionally never registers
// classes in the global RuleIndex, a class-level FindRulesByImportWithConfig always
// misses the index and falls through to CrossResolve. This lets external plugins
// (e.g. proto/wire generators) provide class-level resolutions even when an in-repo
// target owns the enclosing package (a split package).
func (jr *Resolver) resolveClassFromCrossResolver(c *config.Config, pc *javaconfig.Config, className types.ClassName, ix *resolve.RuleIndex, from label.Label) label.Label {
	importSpec := resolve.ImportSpec{Lang: languageName, Imp: className.FullyQualifiedClassName()}
	matches := ix.FindRulesByImportWithConfig(c, importSpec, languageName)
	if len(matches) == 0 {
		return label.NoLabel
	}

	if pc.ResolveToJavaExports() {
		matches = jr.tryResolvingToJavaExport(matches, from)
	}

	candidates := sorted_set.NewSortedSetFn[label.Label]([]label.Label{}, sorted_set.LabelLess)
	for _, m := range matches {
		candidates.Add(m.Label)
	}

	if candidates.Len() == 1 {
		return simplifyLabel(c.RepoName, candidates.SortedSlice()[0], from)
	}

	labelStrings := make([]string, 0, candidates.Len())
	for _, l := range candidates.SortedSlice() {
		labelStrings = append(labelStrings, l.String())
	}
	jr.lang.logger.Error().
		Str("class", className.FullyQualifiedClassName()).
		Strs("targets", labelStrings).
		Msg("cross-resolver returned multiple providers for class")
	return label.NoLabel
}

// tryResolvingToJavaExport attempts to narrow down a list of resolution candidates by preferring java_export targets when appropriate.
// A dependency will be resolved to a `java_export` target when the following are all true.
//   - The dependency is contained in a java_export target, and
//   - There is exactly one java_export target that contains the dependency, and
//   - That java_export does not export the target under consideration (`from`).
//
// Returns a subset of `results`, either by picking an appropriate `java_export`, or by eliminating ineligible `java_export`s.
// The program will issue a fatal error if it finds that more than one java_export contains the required dependency.
func (jr *Resolver) tryResolvingToJavaExport(results []resolve.FindResult, from label.Label) []resolve.FindResult {
	coveredByTheSameExport := func(one, other label.Label) bool {
		oneExport, oneIsCoveredByExport := jr.lang.javaExportIndex.IsExportedByJavaExport(one)
		otherExport, otherIsCoveredByExport := jr.lang.javaExportIndex.IsExportedByJavaExport(other)

		if !oneIsCoveredByExport && !otherIsCoveredByExport {
			return true
		} else if oneIsCoveredByExport && otherIsCoveredByExport {
			return oneExport.Label == otherExport.Label
		}
		return false
	}

	var javaExportsThatCoverThisDep []resolve.FindResult
	var nonJavaExportResults []resolve.FindResult
	for _, result := range results {
		if jr.lang.javaExportIndex.IsJavaExport(result.Label) {
			javaExportsThatCoverThisDep = append(javaExportsThatCoverThisDep, result)
		} else {
			if !coveredByTheSameExport(from, result.Label) {
				dependencyExporter, dependencyIsCovered := jr.lang.javaExportIndex.IsExportedByJavaExport(result.Label)
				if dependencyIsCovered {
					javaExportsThatCoverThisDep = append(javaExportsThatCoverThisDep, resolve.FindResult{Label: dependencyExporter.Label})
				}
			}
			nonJavaExportResults = append(nonJavaExportResults, result)
		}
	}

	if len(javaExportsThatCoverThisDep) == 0 {
		return results
	} else if len(javaExportsThatCoverThisDep) == 1 {
		return javaExportsThatCoverThisDep
	} else if len(javaExportsThatCoverThisDep) > 1 {
		var exportStrings []string
		for _, exportResult := range javaExportsThatCoverThisDep {
			exportStrings = append(exportStrings, exportResult.Label.String())
		}
		jr.lang.logger.Fatal().
			Str("rule", from.Pkg).
			Strs("java_exports", exportStrings).
			Msg("resolveSinglePackage found MULTIPLE java_export targets exporting this rule")
	}

	// If we don't find any relevant java_export, resolve normally.
	return nonJavaExportResults
}

func isJvmLibrary(c *config.Config, kind string) bool {
	return isJavaLibrary(c, kind) || isKotlinLibrary(kind)
}

func isJavaLibrary(c *config.Config, kind string) bool {
	return kind == "java_library" || isJavaProtoLibrary(c, kind)
}

func isKotlinLibrary(kind string) bool {
	return kind == "kt_jvm_library"
}

func isJavaProtoLibrary(c *config.Config, kind string) bool {
	javaProtoLibrary := "java_proto_library"
	javaGrpcLibrary := "java_grpc_library"

	// Check if this kind is mapped FROM a proto library via map_kind
	for _, mappedKind := range c.KindMap {
		if mappedKind.KindName == kind {
			if mappedKind.FromKind == javaProtoLibrary {
				javaProtoLibrary = kind
				break
			}

			if mappedKind.FromKind == javaGrpcLibrary {
				javaGrpcLibrary = kind
				break
			}
		}
	}

	return kind == javaProtoLibrary || kind == javaGrpcLibrary
}
