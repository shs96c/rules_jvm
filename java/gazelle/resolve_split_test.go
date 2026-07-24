package gazelle

import (
	"path/filepath"
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/java"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/sorted_set"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

// TestSplitPackageTestSuiteHelperResolution covers a test helper whose package
// has multiple production providers. Package resolution is ambiguous in that
// case, so class-level resolution must still consider the helper library emitted
// by java_test_suite.
func TestSplitPackageTestSuiteHelperResolution(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in test config")
	}

	javaPackage := types.NewPackageName("com.example.shared")
	productionContent := `java_library(
    name = "one",
    _packages = ["com.example.shared"],
)

java_library(
    name = "two",
    _packages = ["com.example.shared"],
)
`
	productionFile, err := rule.LoadData(filepath.Join("production", "BUILD.bazel"), "production", []byte(productionContent))
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range productionFile.Rules {
		setPackagesPrivateAttr(r)
		providerLabel := label.New("", "production", r.Name())
		jLang.classExportCache[providerLabel.String()] = classExportInfo{
			classes:  []types.ClassName{types.NewClassName(javaPackage, []string{"One", "Two"}[i])},
			testonly: false,
		}
		ix.AddRule(c, r, productionFile)
	}

	suiteContent := `java_test_suite(
    name = "suite-one",
)

java_test_suite(
    name = "suite-two",
)
`
	suiteFile, err := rule.LoadData(filepath.Join("helpers", "BUILD.bazel"), "helpers", []byte(suiteContent))
	if err != nil {
		t.Fatal(err)
	}
	for i, suiteRule := range suiteFile.Rules {
		suiteRule.SetPrivateAttr(packagesKey, []types.ResolvableJavaPackage{
			*types.NewResolvableJavaPackage(javaPackage, true, true),
		})
		helperLabel := label.New("", "helpers", suiteRule.Name()+"-test-lib")
		jLang.classExportCache[helperLabel.String()] = classExportInfo{
			classes:  []types.ClassName{types.NewClassName(javaPackage, []string{"OtherHelper", "Helper"}[i])},
			testonly: true,
		}
		ix.AddRule(c, suiteRule, suiteFile)
	}
	ix.Finish()

	productionSpec := resolve.ImportSpec{Lang: languageName, Imp: types.NewResolvableJavaPackage(javaPackage, false, false).String()}
	if got := len(ix.FindRulesByImportWithConfig(c, productionSpec, languageName)); got != 2 {
		t.Fatalf("test precondition violated: got %d production providers, want 2", got)
	}

	importerContent := `java_test_suite(
    name = "consumer",
)
`
	importerFile, err := rule.LoadData("BUILD.bazel", "", []byte(importerContent))
	if err != nil {
		t.Fatal(err)
	}
	importerRule := importerFile.Rules[0]
	resolveInput := types.ResolveInput{
		PackageNames:         testPackageNames(),
		ImportedPackageNames: testPackageNames(javaPackage),
		ImportedClasses:      testClassNames(types.NewClassName(javaPackage, "Helper")),
		ExportedPackageNames: testPackageNames(),
		ExportedClassNames:   testClassNames(),
		AnnotationProcessors: testClassNames(),
	}

	mrslv.Resolver(importerRule, "").Resolve(c, ix, rc, importerRule, resolveInput, label.New("", "", "consumer"))

	got := importerRule.AttrStrings("deps")
	if len(got) != 1 || got[0] != "//helpers:suite-two-test-lib" {
		t.Errorf("deps mismatch: got %v, want [//helpers:suite-two-test-lib]", got)
	}
}

func TestModuleSccSplitPackageDeclaredClassesResolveToOwningGroup(t *testing.T) {
	c, langs, _ := testConfig(t)
	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in test config")
	}

	const moduleRoot = "src/main/java"
	rootConfig := javaconfig.New(c.RepoRoot)
	if err := rootConfig.SetModuleGranularity("scc"); err != nil {
		t.Fatal(err)
	}
	configs := c.Exts[languageName].(javaconfig.Configs)
	configs[moduleRoot] = rootConfig
	for _, rel := range []string{
		moduleRoot + "/consumer",
		moduleRoot + "/model",
		moduleRoot + "/util",
	} {
		configs[rel] = rootConfig.NewChild()
	}

	sharedPackage := types.NewPackageName("com.example.creditlines")
	topLevelFunction := types.NewClassName(sharedPackage, "calculationForTest")
	secondaryClass := types.NewClassName(sharedPackage, "CreditLineSnapshot")
	consumerClass := types.NewClassName(sharedPackage, "Consumer")
	jLang.javaPackageCache = map[string]*java.Package{
		moduleRoot + "/consumer": {
			Name: sharedPackage,
			DeclaredClasses: sorted_set.NewSortedSetFn([]types.ClassName{
				consumerClass,
			}, types.ClassNameLess),
			ImportedClasses: sorted_set.NewSortedSetFn([]types.ClassName{
				topLevelFunction,
				secondaryClass,
			}, types.ClassNameLess),
			Files: sorted_set.NewSortedSet([]string{"Consumer.kt"}),
		},
		moduleRoot + "/model": {
			Name: sharedPackage,
			DeclaredClasses: sorted_set.NewSortedSetFn([]types.ClassName{
				secondaryClass,
			}, types.ClassNameLess),
			Files: sorted_set.NewSortedSet([]string{"Models.kt"}),
		},
		moduleRoot + "/util": {
			Name: sharedPackage,
			DeclaredClasses: sorted_set.NewSortedSetFn([]types.ClassName{
				topLevelFunction,
			}, types.ClassNameLess),
			Files: sorted_set.NewSortedSet([]string{"CreditLineUtilities.kt"}),
		},
	}

	buildFile := rule.EmptyFile(moduleRoot+"/BUILD.bazel", moduleRoot)
	result := language.GenerateResult{}
	jLang.emitModuleProductionLibraries(
		language.GenerateArgs{Config: c, File: buildFile, Rel: moduleRoot},
		rootConfig,
		sorted_set.NewSortedSet([]string{}),
		"",
		"",
		&result,
		jLang.logger,
	)

	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)
	var consumerRule *rule.Rule
	var consumerResolveInput types.ResolveInput
	for i, generatedRule := range result.Gen {
		generatedRule.Insert(buildFile)
		ix.AddRule(c, generatedRule, buildFile)
		if generatedRule.Name() == "consumer" {
			consumerRule = generatedRule
			consumerResolveInput = result.Imports[i].(types.ResolveInput)
		}
	}
	if consumerRule == nil {
		t.Fatal("consumer rule was not generated")
	}
	ix.Finish()

	mrslv.Resolver(consumerRule, "").Resolve(c, ix, rc, consumerRule, consumerResolveInput, label.New("", moduleRoot, "consumer"))

	if got := consumerRule.AttrStrings("deps"); len(got) != 0 {
		t.Errorf("deps mismatch: got %v, want []", got)
	}
	got := consumerRule.AttrStrings("associates")
	want := []string{":model", ":util"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("associates mismatch: got %v, want %v", got, want)
	}
}

func testPackageNames(values ...types.PackageName) *sorted_set.SortedSet[types.PackageName] {
	return sorted_set.NewSortedSetFn(values, types.PackageNameLess)
}

func testClassNames(values ...types.ClassName) *sorted_set.SortedSet[types.ClassName] {
	return sorted_set.NewSortedSetFn(values, types.ClassNameLess)
}
