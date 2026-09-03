package gazelle

import (
	"strings"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/rule"
	bzl "github.com/bazelbuild/buildtools/build"
)

// Resolution needs the kept friends before the merge restores them.
func copyExistingAssociates(file *rule.File, generated *rule.Rule) {
	if file == nil || !ruleHasKotlinSources(generated) {
		return
	}
	for _, existing := range file.Rules {
		if existing.Name() != generated.Name() || existing.Attr("associates") == nil {
			continue
		}
		generated.SetAttr("associates", existing.Attr("associates"))
		*generated.AttrComments("associates") = *existing.AttrComments("associates")
		return
	}
}

func keptKotlinAssociates(r *rule.Rule) ([]string, bool) {
	if r.Attr("associates") == nil {
		return nil, false
	}
	attribute := &bzl.AssignExpr{Comments: *r.AttrComments("associates")}
	if rule.ShouldKeep(attribute) || rule.ShouldKeep(r.Attr("associates")) {
		return r.AttrStrings("associates"), true
	}
	list, ok := r.Attr("associates").(*bzl.ListExpr)
	if !ok {
		return nil, false
	}
	var kept []string
	for _, entry := range list.List {
		if value, ok := entry.(*bzl.StringExpr); ok && rule.ShouldKeep(value) {
			kept = append(kept, value.Value)
		}
	}
	return kept, false
}

// populateProductionAssociatesAttr preserves Kotlin's module-wide `internal` across the
// fine-grained targets that scc granularity produces. An explicit kotlin module name can
// extend the same logical module across nested Bazel package ownership boundaries, so:
//
//   - a non-leaf (one with same-package kt_jvm_library deps) friends those deps via
//     `associates`, moving them out of `deps`, and adopts their shared module_name -- so it
//     must NOT also set module_name (rules_kotlin forbids both); and
//   - a leaf (no same-package kt deps) pins an explicit module_name (the package path, a
//     stable id every target in the module shares) so the non-leaves that associate it agree
//     on one Kotlin module.
//
// rules_kotlin requires all of a target's associates to share one module_name and analyses
// deps bottom-up, so the leaves' shared module_name propagates to every target. Without an
// explicit module name only scc granularity is touched; the directive deliberately opts
// package/module-granularity descendants into the named logical module.
func (jr *Resolver) populateProductionAssociatesAttr(c *config.Config, r *rule.Rule, from label.Label) {
	configs := c.Exts[languageName].(javaconfig.Configs)
	pc := configs[from.Pkg]
	if pc == nil || (pc.ModuleGranularity() != "scc" && pc.KotlinModuleName() == "") {
		return
	}

	var associates, kept []string
	for _, dep := range r.AttrStrings("deps") {
		parsed, err := label.Parse(dep)
		if err != nil {
			kept = append(kept, dep)
			continue
		}
		abs := parsed.Abs(from.Repo, from.Pkg)
		if abs.Repo == from.Repo &&
			jr.lang.kotlinLibraries[label.New("", abs.Pkg, abs.Name).String()] &&
			productionKotlinModulesMatch(pc, configs[abs.Pkg], from.Pkg, abs.Pkg) {
			associates = append(associates, dep)
		} else {
			kept = append(kept, dep)
		}
	}

	if len(associates) > 0 {
		// Non-leaf: friend same-module deps; it adopts their module_name, so it must not set one.
		replaceStringListAttr(r, "associates", associates)
		r.DelAttr("module_name")
		replaceStringListAttr(r, "deps", kept)
		return
	}

	// Leaf: pin the shared module name and clear any stale associates.
	moduleName := pc.KotlinModuleName()
	if moduleName == "" {
		moduleName = strings.ReplaceAll(from.Pkg, "/", "_")
	}
	r.SetAttr("module_name", moduleName)
	r.DelAttr("associates")
}

func productionKotlinModulesMatch(fromConfig, dependencyConfig *javaconfig.Config, fromPkg, dependencyPkg string) bool {
	if fromConfig.KotlinModuleName() != "" {
		return dependencyConfig != nil && dependencyConfig.KotlinModuleName() == fromConfig.KotlinModuleName()
	}
	return fromConfig.ModuleGranularity() == "scc" && fromPkg == dependencyPkg
}

// kotlinModuleIdentity is deliberately conservative when no explicit logical
// module has been configured. SCC targets in one Bazel package share the module
// name populated above. Other generated Kotlin libraries use rules_kotlin's
// label-derived default and are therefore distinct modules.
func kotlinModuleIdentity(configs javaconfig.Configs, target label.Label) string {
	if pc := configs[target.Pkg]; pc != nil {
		if name := pc.KotlinModuleName(); name != "" {
			return "configured:" + name
		}
		if pc.ModuleGranularity() == "scc" {
			return "scc:" + target.Pkg
		}
	}
	return "target:" + target.String()
}
