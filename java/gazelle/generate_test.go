package gazelle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/java"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/java_export_index"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/sorted_set"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/language/proto"
	"github.com/bazelbuild/bazel-gazelle/merger"
	"github.com/bazelbuild/bazel-gazelle/rule"
	bzl "github.com/bazelbuild/buildtools/build"
	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSccCleanupRetainsRegeneratedLibraries(t *testing.T) {
	f, err := rule.LoadData("BUILD.bazel", "", []byte(`
kt_jvm_library(name = "live", srcs = ["Live.kt"], associates = [":dependency"])
java_library(name = "removed", srcs = ["Removed.java"])
`))
	require.NoError(t, err)
	live := rule.NewRule("kt_jvm_library", "live")
	live.SetAttr("srcs", []string{"Live.kt"})
	res := language.GenerateResult{Gen: []*rule.Rule{live}}

	markStaleSCCLibrariesForDeletion(f, &res)

	require.Len(t, res.Empty, 1)
	require.Equal(t, "removed", res.Empty[0].Name())
}

func TestSingleJavaTestFile(t *testing.T) {
	f := javaFile{
		pathRelativeToBazelWorkspaceRoot: "FooTest.java",
		pkg:                              types.NewPackageName("com.example"),
	}
	type testCase struct {
		includePackageInName bool
		importedPackages     []string
		wrapper              string
		wantRuleKind         string
		wantImports          []string
		wantDeps             []string
		wantRuntimeDeps      []string
		wantArgs             []bzl.Expr
	}

	for name, tc := range map[string]testCase{
		"no imported packages no helpers no package": {
			includePackageInName: false,
			importedPackages:     nil,
			wantRuleKind:         "java_test",
			wantImports:          []string{"com.example"},
			wantDeps:             nil,
		},
		"some imported packages no helpers no package": {
			includePackageInName: false,
			importedPackages:     []string{"io.netty"},
			wantRuleKind:         "java_test",
			wantImports:          []string{"io.netty", "com.example"},
			wantDeps:             nil,
		},
		"no imported packages some helpers no package": {
			includePackageInName: false,
			importedPackages:     nil,
			wantRuleKind:         "java_test",
			wantImports:          []string{"com.example"},
			wantDeps:             []string{":helper"},
		},
		"some imported packages some helpers no package": {
			includePackageInName: false,
			importedPackages:     []string{"io.netty"},
			wantRuleKind:         "java_test",
			wantImports:          []string{"io.netty", "com.example"},
		},
		"no imported packages no helpers yes package": {
			includePackageInName: true,
			importedPackages:     nil,
			wantRuleKind:         "java_test",
			wantImports:          []string{"com.example"},
			wantDeps:             nil,
		},
		"some imported packages no helpers yes package": {
			includePackageInName: true,
			importedPackages:     []string{"io.netty"},
			wantRuleKind:         "java_test",
			wantImports:          []string{"io.netty", "com.example"},
			wantDeps:             nil,
		},
		"no imported packages some helpers yes package": {
			includePackageInName: true,
			importedPackages:     nil,
			wantRuleKind:         "java_test",
			wantImports:          []string{"com.example"},
			wantDeps:             []string{":helper"},
		},
		"some imported packages some helpers yes package": {
			includePackageInName: true,
			importedPackages:     []string{"io.netty"},
			wantRuleKind:         "java_test",
			wantImports:          []string{"io.netty", "com.example"},
		},
		"explicit junit4": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit"},
			wantRuleKind:         "java_test",
			wantImports:          []string{"com.example", "org.junit"},
		},
		"wrapper junit4": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit"},
			wrapper:              "some_wrapper",
			wantRuleKind:         "some_wrapper",
			wantImports:          []string{"com.example", "org.junit"},
			wantArgs:             []bzl.Expr{&bzl.Ident{Name: "java_test"}},
		},
		"explicit junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit.jupiter.api"},
			wantRuleKind:         "java_junit5_test",
			wantImports:          []string{"com.example", "org.junit.jupiter.api"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
			},
		},
		"parameterized junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit.jupiter.params"},
			wantRuleKind:         "java_junit5_test",
			wantImports:          []string{"com.example", "org.junit.jupiter.params"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
			},
		},
		"junitpioneer junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junitpioneer.jupiter.cartesian"},
			wantRuleKind:         "java_junit5_test",
			wantImports:          []string{"com.example", "org.junitpioneer.jupiter.cartesian"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
			},
		},
		"wrapper junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit.jupiter.api"},
			wrapper:              "some_wrapper",
			wantRuleKind:         "some_wrapper",
			wantImports:          []string{"com.example", "org.junit.jupiter.api"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
			},
			wantArgs: []bzl.Expr{&bzl.Ident{Name: "java_junit5_test"}},
		},
		"explicit both junit4 and junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit", "org.junit.jupiter.api"},
			wantRuleKind:         "java_junit5_test",
			wantImports:          []string{"com.example", "org.junit", "org.junit.jupiter.api"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var res language.GenerateResult

			l := newTestJavaLang(t)
			l.generateJavaTest(nil, "", "maven", f, tc.includePackageInName, stringsToPackageNames(tc.importedPackages), nil, nil, nil, tc.wrapper, nil, &res)

			require.Len(t, res.Gen, 1, "want 1 generated rule")

			rule := res.Gen[0]
			require.Equal(t, tc.wantRuleKind, rule.Kind())
			if tc.includePackageInName {
				require.Equal(t, "com_example_FooTest", rule.AttrString("name"))
			} else {
				require.Equal(t, "FooTest", rule.AttrString("name"))
			}
			require.Equal(t, []string{"FooTest.java"}, rule.AttrStrings("srcs"))
			require.Equal(t, "com.example.FooTest", rule.AttrString("test_class"))

			wantAttrs := []string{"name", "srcs", "test_class"}
			if len(tc.wantRuntimeDeps) > 0 {
				wantAttrs = append(wantAttrs, "runtime_deps")
			}
			require.ElementsMatch(t, wantAttrs, rule.AttrKeys())
			require.ElementsMatch(t, tc.wantArgs, rule.Args())

			require.Len(t, res.Imports, 1, "want 1 generated importedPackages")
			wantImports := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
			for _, wi := range tc.wantImports {
				wantImports.Add(types.NewPackageName(wi))
			}
			require.ElementsMatch(t, wantImports.SortedSlice(), res.Imports[0].(types.ResolveInput).ImportedPackageNames.SortedSlice())

			if len(tc.wantRuntimeDeps) > 0 {
				require.ElementsMatch(t, tc.wantRuntimeDeps, rule.AttrStrings("runtime_deps"))
			}
		})
	}
}

func TestNestedMainInTestHelperProducesDeletionStub(t *testing.T) {
	pkg := types.NewPackageName("com.example")
	mainClass := types.NewClassName(pkg, "PaymentsFeatureFlagTestRunner.PaymentsFeatureFlagApp")
	allMains := sorted_set.NewSortedSetFn([]types.ClassName{mainClass}, types.ClassNameLess)
	testSources := sorted_set.NewSortedSetFn([]javaFile{{
		pathRelativeToBazelWorkspaceRoot: "src/test/java/com/example/PaymentsFeatureFlagTestRunner.java",
		pkg:                              pkg,
	}}, javaFileLess)
	result := language.GenerateResult{}

	javaLang{}.processJavaBinary(&rule.File{}, "src/test/java/com/example", allMains, testSources, nil, &result, javaconfig.New("."))

	require.Empty(t, result.Gen)
	require.Len(t, result.Empty, 1)
	require.Equal(t, "java_binary", result.Empty[0].Kind())
	require.Equal(t, "PaymentsFeatureFlagTestRunner.PaymentsFeatureFlagApp", result.Empty[0].Name())
}

func TestTopLevelKotlinMainInTestHelperProducesDeletionStub(t *testing.T) {
	pkg := types.NewPackageName("com.example")
	mainClass := types.NewClassName(pkg, "GenerateRecoveryReplayFixturesKt")
	allMains := sorted_set.NewSortedSetFn([]types.ClassName{mainClass}, types.ClassNameLess)
	testSources := sorted_set.NewSortedSetFn([]javaFile{{
		pathRelativeToBazelWorkspaceRoot: "src/test/kotlin/com/example/GenerateRecoveryReplayFixtures.kt",
		pkg:                              pkg,
	}}, javaFileLess)
	result := language.GenerateResult{}

	javaLang{}.processJavaBinary(&rule.File{}, "src/test/kotlin/com/example", allMains, testSources, nil, &result, javaconfig.New("."))

	require.Empty(t, result.Gen)
	require.Len(t, result.Empty, 1)
	require.Equal(t, "GenerateRecoveryReplayFixturesKt", result.Empty[0].Name())
}

func TestNestedMainInTestSourceUsesProductionClassNameShape(t *testing.T) {
	pkg := types.NewPackageName("com.example")
	// The parser returns a package-relative "Outer.Inner" string which the Go
	// adapter passes to NewClassName, rather than ParseClassName.
	mainClass := types.NewClassName(pkg, "FooTest.App")
	allMains := sorted_set.NewSortedSetFn([]types.ClassName{mainClass}, types.ClassNameLess)
	testFiles := sorted_set.NewSortedSetFn([]javaFile{{
		pathRelativeToBazelWorkspaceRoot: "src/test/java/com/example/FooTest.java",
		pkg:                              pkg,
	}}, javaFileLess)
	result := language.GenerateResult{}

	javaLang{}.processJavaBinary(&rule.File{}, "src/test/java/com/example", allMains, testFiles, nil, &result, javaconfig.New("."))

	require.Empty(t, result.Gen)
	require.Len(t, result.Empty, 1)
	require.Equal(t, "FooTest.App", result.Empty[0].Name())
}

func TestSuiteOwnedMainCleanupRunsWhenBinaryGenerationDisabled(t *testing.T) {
	pkg := types.NewPackageName("com.example")
	mainClass := types.NewClassName(pkg, "TestApp")
	allMains := sorted_set.NewSortedSetFn([]types.ClassName{mainClass}, types.ClassNameLess)
	testSources := sorted_set.NewSortedSetFn([]javaFile{{
		pathRelativeToBazelWorkspaceRoot: "src/test/java/com/example/TestApp.java",
		pkg:                              pkg,
	}}, javaFileLess)
	cfg := javaconfig.New(".")
	cfg.SetGenerateBinary(false)
	result := language.GenerateResult{}

	javaLang{}.processJavaBinary(&rule.File{}, "src/test/java/com/example", allMains, testSources, nil, &result, cfg)

	require.Empty(t, result.Gen)
	require.Len(t, result.Empty, 1)
	require.Equal(t, "TestApp", result.Empty[0].Name())
}

func TestSuiteOwnedMainDeletionPreservesCustomNamedBinary(t *testing.T) {
	pkg := types.NewPackageName("com.example")
	mainClass := types.NewClassName(pkg, "TestApp")
	allMains := sorted_set.NewSortedSetFn([]types.ClassName{mainClass}, types.ClassNameLess)
	testSources := sorted_set.NewSortedSetFn([]javaFile{{
		pathRelativeToBazelWorkspaceRoot: "src/test/java/com/example/TestApp.java",
		pkg:                              pkg,
	}}, javaFileLess)
	file := rule.EmptyFile("BUILD", "src/test/java/com/example")
	intentional := rule.NewRule("java_binary", "custom_test_app")
	intentional.SetAttr("main_class", "com.example.TestApp")
	intentional.Insert(file)
	result := language.GenerateResult{}

	javaLang{}.processJavaBinary(file, "src/test/java/com/example", allMains, testSources, nil, &result, javaconfig.New("."))

	require.Len(t, result.Empty, 1)
	require.Equal(t, "TestApp", result.Empty[0].Name())
	require.NotEqual(t, intentional.Name(), result.Empty[0].Name())
	require.Equal(t, "custom_test_app", file.Rules[0].Name())
}

func TestSuiteOwnedMainCleanupRespectsKeptRuntimeDeps(t *testing.T) {
	for name, keep := range map[string]string{"kept": " # keep", "unkept": ""} {
		t.Run(name, func(t *testing.T) {
			file, err := rule.LoadData("BUILD.bazel", "src/test/java/com/example", []byte(`
java_binary(
    name = "TestApp",
    main_class = "com.example.TestApp",
    runtime_deps = [":example-test-lib"],`+keep+`
)
`))
			require.NoError(t, err)
			pkg := types.NewPackageName("com.example")
			mains := sorted_set.NewSortedSetFn([]types.ClassName{
				types.NewClassName(pkg, "TestApp"),
			}, types.ClassNameLess)
			sources := sorted_set.NewSortedSetFn([]javaFile{{
				pathRelativeToBazelWorkspaceRoot: "src/test/java/com/example/TestApp.java",
				pkg:                              pkg,
			}}, javaFileLess)
			lang := NewLanguage().(*javaLang)
			res := language.GenerateResult{}
			lang.processJavaBinary(file, file.Pkg, mains, sources, nil, &res, javaconfig.New("."))
			for _, phase := range []merger.Phase{merger.PreResolve, merger.PostResolve} {
				merger.MergeFile(file, res.Empty, res.Gen, phase, lang.Kinds(), nil)
			}
			file.Sync()
			if keep == "" {
				require.Empty(t, file.Rules)
			} else {
				require.Len(t, file.Rules, 1)
				require.Equal(t, []string{":example-test-lib"}, file.Rules[0].AttrStrings("runtime_deps"))
			}
		})
	}
}

func TestProductionMainStillGeneratesBinary(t *testing.T) {
	pkg := types.NewPackageName("com.example")
	mainClass := types.NewClassName(pkg, "App")
	allMains := sorted_set.NewSortedSetFn([]types.ClassName{mainClass}, types.ClassNameLess)
	result := language.GenerateResult{}

	javaLang{}.processJavaBinary(&rule.File{}, "src/main/java/com/example", allMains, nil, nil, &result, javaconfig.New("."))

	require.Empty(t, result.Empty)
	require.Len(t, result.Gen, 1)
	require.Equal(t, "App", result.Gen[0].Name())
	require.Equal(t, "com.example.App", result.Gen[0].AttrString("main_class"))
	require.Equal(t, []string{":example"}, result.Gen[0].AttrStrings("runtime_deps"))
}

func TestSccProductionMainsDependOnOwningGroup(t *testing.T) {
	const moduleRoot = "src/main/java"
	alphaPackage := types.NewPackageName("com.example.alpha")
	betaPackage := types.NewPackageName("com.example.beta")
	alphaMain := types.NewClassName(alphaPackage, "AlphaApp")
	betaMain := types.NewClassName(betaPackage, "BetaApp")
	packageWithMain := func(pkg types.PackageName, main types.ClassName, filename string) *java.Package {
		return &java.Package{
			Name:            pkg,
			DeclaredClasses: sorted_set.NewSortedSetFn([]types.ClassName{main}, types.ClassNameLess),
			Files:           sorted_set.NewSortedSet([]string{filename}),
			Mains:           sorted_set.NewSortedSetFn([]types.ClassName{main}, types.ClassNameLess),
		}
	}

	c, langs, _ := testConfig(t)
	rootConfig := javaconfig.New(c.RepoRoot)
	require.NoError(t, rootConfig.SetModuleGranularity("scc"))
	configs := c.Exts[languageName].(javaconfig.Configs)
	configs[moduleRoot] = rootConfig
	configs[moduleRoot+"/com"] = rootConfig.NewChild()
	configs[moduleRoot+"/com/example"] = rootConfig.NewChild()
	configs[moduleRoot+"/com/example/alpha"] = rootConfig.NewChild()
	configs[moduleRoot+"/com/example/beta"] = rootConfig.NewChild()

	l := langs[1].(*javaLang)
	l.javaPackageCache = map[string]*java.Package{
		moduleRoot + "/com/example/alpha": packageWithMain(alphaPackage, alphaMain, "AlphaApp.java"),
		moduleRoot + "/com/example/beta":  packageWithMain(betaPackage, betaMain, "BetaApp.java"),
	}

	file, err := rule.LoadData(moduleRoot+"/BUILD.bazel", moduleRoot, []byte(`java_binary(
    name = "AlphaApp",
    main_class = "com.example.alpha.AlphaApp",
    runtime_deps = [
        ":java",
        ":kept_runtime",  # keep
        "//runtime:reviewed",
    ],
)
`))
	require.NoError(t, err)
	args := language.GenerateArgs{Config: c, File: file, Rel: moduleRoot}
	result := language.GenerateResult{}
	mainLibraryNames := l.emitModuleProductionLibraries(
		args,
		rootConfig,
		sorted_set.NewSortedSet([]string{}),
		"",
		"",
		&result,
		l.logger,
	)
	allMains := sorted_set.NewSortedSetFn(
		[]types.ClassName{alphaMain, betaMain},
		types.ClassNameLess,
	)
	l.processJavaBinary(file, moduleRoot, allMains, nil, mainLibraryNames, &result, rootConfig)

	require.Equal(t, map[string]string{
		"com.example.alpha.AlphaApp": "alpha",
		"com.example.beta.BetaApp":   "beta",
	}, mainLibraryNames)
	generated := make(map[string]*rule.Rule)
	for _, generatedRule := range result.Gen {
		generated[generatedRule.Name()] = generatedRule
	}
	require.Contains(t, generated, "AlphaApp")
	require.Contains(t, generated, "BetaApp")
	require.ElementsMatch(t, []string{":alpha", ":kept_runtime", "//runtime:reviewed"}, generated["AlphaApp"].AttrStrings("runtime_deps"))
	require.Equal(t, []string{":beta"}, generated["BetaApp"].AttrStrings("runtime_deps"))
	require.NotContains(t, generated["AlphaApp"].AttrStrings("runtime_deps"), ":java")
	require.NotContains(t, generated["BetaApp"].AttrStrings("runtime_deps"), ":java")
}

func TestSccLibrariesIndexParserDeclaredClasses(t *testing.T) {
	const moduleRoot = "src/main/kotlin"
	pkg := types.NewPackageName("com.example")
	declared := []types.ClassName{
		types.NewClassName(pkg, "Secondary"),
		types.NewClassName(pkg, "topLevelFunction"),
	}
	c, langs, _ := testConfig(t)
	cfg := javaconfig.New(c.RepoRoot)
	require.NoError(t, cfg.SetModuleGranularity("scc"))
	configs := c.Exts[languageName].(javaconfig.Configs)
	configs[moduleRoot] = cfg
	configs[moduleRoot+"/com"] = cfg.NewChild()
	configs[moduleRoot+"/com/example"] = cfg.NewChild()
	l := langs[1].(*javaLang)
	l.javaPackageCache = map[string]*java.Package{
		moduleRoot + "/com/example": {
			Name:            pkg,
			Files:           sorted_set.NewSortedSet([]string{"Declarations.kt"}),
			DeclaredClasses: sorted_set.NewSortedSetFn(declared, types.ClassNameLess),
		},
	}
	result := language.GenerateResult{}
	l.emitModuleProductionLibraries(
		language.GenerateArgs{Config: c, Rel: moduleRoot},
		cfg, sorted_set.NewSortedSet([]string{}), "", "", &result, l.logger,
	)

	require.Len(t, result.Gen, 1)
	for _, class := range declared {
		require.Contains(t, result.Gen[0].PrivateAttr(classesKey), class)
	}
}

func TestResourcesFollowSourceSetOwnership(t *testing.T) {
	for name, tc := range map[string]struct {
		rel          string
		files        []string
		resourceKind string
		generated    []string
		empty        []string
	}{
		"source-set resources produce targets": {
			rel: "src/main/resources", files: []string{"data.txt"},
			generated: []string{"resources", "resources_lib"},
		},
		"empty source-set resources remove stale targets": {
			rel: "src/main/resources", empty: []string{"resources", "resources_lib"},
		},
		"empty handwritten resources retain their wrapper": {
			rel: "src/main/resources", resourceKind: "filegroup",
		},
		"nonempty handwritten resources are not replaced": {
			rel: "src/main/resources", resourceKind: "filegroup", files: []string{"data.txt"},
		},
		"nested resources directory is not a source-set root": {
			rel: "src/main/resources/nested/resources", files: []string{"data.txt"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, langs, _ := testConfig(t)
			c.RepoRoot = t.TempDir()
			dir := filepath.Join(c.RepoRoot, tc.rel)
			require.NoError(t, os.MkdirAll(dir, 0755))
			for _, file := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("data"), 0644))
			}
			cfg := javaconfig.New(c.RepoRoot)
			cfg.SetSourcesetRoot("src/main")
			cfg.SetGenerateProto(false)
			c.Exts[languageName].(javaconfig.Configs)[tc.rel] = cfg
			var file *rule.File
			if tc.resourceKind != "" {
				var err error
				file, err = rule.LoadData("BUILD.bazel", tc.rel, []byte(tc.resourceKind+"(name = \"resources\")"))
				require.NoError(t, err)
			}
			result := langs[1].(*javaLang).GenerateRules(language.GenerateArgs{
				Config: c, Dir: dir, Rel: tc.rel, RegularFiles: tc.files, File: file,
			})
			var generated, empty []string
			for _, r := range result.Gen {
				generated = append(generated, r.Name())
			}
			for _, r := range result.Empty {
				empty = append(empty, r.Name())
			}
			require.ElementsMatch(t, tc.generated, generated)
			require.ElementsMatch(t, tc.empty, empty)
		})
	}
}

func TestSuite(t *testing.T) {
	src := "FooTest.java"
	pkg := "com.example"

	type testCase struct {
		includePackageInName bool
		importedPackages     []string
		wantImports          []string
		wantDeps             []string
		wantRuntimeDeps      []string
		wantRunner           string
	}

	for name, tc := range map[string]testCase{
		"explicit junit4": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit"},
			wantImports:          []string{"com.example", "org.junit"},
		},
		"explicit junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit.jupiter.api"},
			wantImports:          []string{"com.example", "org.junit.jupiter.api"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
			},
			wantRunner: "junit5",
		},
		"parameterized junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit.jupiter.params"},
			wantImports:          []string{"com.example", "org.junit.jupiter.params"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
			},
			wantRunner: "junit5",
		},
		"explicit both junit4 and junit5": {
			includePackageInName: false,
			importedPackages:     []string{"org.junit", "org.junit.jupiter.api"},
			wantImports:          []string{"com.example", "org.junit", "org.junit.jupiter.api"},
			wantRuntimeDeps: []string{
				"@maven//:org_junit_jupiter_junit_jupiter_engine",
				"@maven//:org_junit_platform_junit_platform_launcher",
				"@maven//:org_junit_platform_junit_platform_reporting",
				"@maven//:org_junit_vintage_junit_vintage_engine",
			},
			wantRunner: "junit5",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var res language.GenerateResult

			l := newTestJavaLang(t)
			l.generateJavaTestSuite(nil, "blah", []string{src}, stringsToPackageNames([]string{pkg}), "maven", stringsToPackageNames(tc.importedPackages), nil, nil, nil, false, &res)

			require.Len(t, res.Gen, 1, "want 1 generated rule")

			rule := res.Gen[0]
			require.Equal(t, "java_test_suite", rule.Kind())
			require.Equal(t, "blah", rule.AttrString("name"))
			require.Equal(t, []string{"FooTest.java"}, rule.AttrStrings("srcs"))

			wantAttrs := []string{"name", "srcs"}
			if len(tc.wantRuntimeDeps) > 0 {
				wantAttrs = append(wantAttrs, "runtime_deps")
			}
			if tc.wantRunner != "" {
				wantAttrs = append(wantAttrs, "runner")
			}
			require.ElementsMatch(t, wantAttrs, rule.AttrKeys())

			require.Len(t, res.Imports, 1, "want 1 generated importedPackages")
			wantImports := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
			for _, wi := range tc.wantImports {
				wantImports.Add(types.NewPackageName(wi))
			}
			require.ElementsMatch(t, wantImports.SortedSlice(), res.Imports[0].(types.ResolveInput).ImportedPackageNames.SortedSlice())

			if len(tc.wantRuntimeDeps) > 0 {
				require.ElementsMatch(t, tc.wantRuntimeDeps, rule.AttrStrings("runtime_deps"))
			}

			if tc.wantRunner != "" {
				require.Equal(t, tc.wantRunner, rule.AttrString("runner"))
			}
		})
	}
}

func TestSuitePreservesExistingPlugins(t *testing.T) {
	f, err := rule.LoadData("BUILD.bazel", "", []byte(`
GenJavaTests(
    name = "blah",
    plugins = ["//processors:processor"],
)
`))
	require.NoError(t, err)

	var res language.GenerateResult
	l := newTestJavaLang(t)
	l.generateJavaTestSuite(
		f,
		"blah",
		[]string{"FooTest.java"},
		stringsToPackageNames([]string{"com.example"}),
		"maven",
		stringsToPackageNames([]string{"org.junit"}),
		nil, nil, nil, false,
		&res,
	)

	require.Len(t, res.Gen, 1)
	require.Equal(t, []string{"//processors:processor"}, res.Gen[0].AttrStrings("plugins"))
}

func TestCollisionFreeTestSuiteName(t *testing.T) {
	cfg := javaconfig.New(".")

	for name, tc := range map[string]struct {
		dirname                 string
		concreteTestTargetNames map[string]struct{}
		wantSuite               string
		wantGeneratedLibraries  []string
	}{
		"suite name matches concrete test target": {
			dirname: "CATest",
			concreteTestTargetNames: map[string]struct{}{
				"CATest": {},
			},
			wantSuite: "CATest-tests",
			wantGeneratedLibraries: []string{
				"CATest-tests-test-lib",
				"CATest-tests-test-deps-lib",
				"CATest-tests-test-runtime-deps-lib",
			},
		},
		"helper-only suite has no concrete test target": {
			dirname:                 "AggregateExtensionsTest",
			concreteTestTargetNames: map[string]struct{}{},
			wantSuite:               "AggregateExtensionsTest",
			wantGeneratedLibraries: []string{
				"AggregateExtensionsTest-test-lib",
				"AggregateExtensionsTest-test-deps-lib",
				"AggregateExtensionsTest-test-runtime-deps-lib",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			gotSuite := collisionFreeTestSuiteName(cfg, tc.dirname, false, tc.concreteTestTargetNames)
			require.Equal(t, tc.wantSuite, gotSuite)
			require.Equal(t, tc.wantGeneratedLibraries, []string{
				testHelperLibname(gotSuite),
				gotSuite + "-test-deps-lib",
				gotSuite + "-test-runtime-deps-lib",
			})
		})
	}
}

func TestTransitionExistingLibraryKind(t *testing.T) {
	f, err := rule.LoadData("BUILD.bazel", "", []byte(`
java_library(name = "kotlin")
# keep
java_library(name = "kept")
`))
	require.NoError(t, err)

	transitionExistingLibraryKind(f, "kotlin", "kt_jvm_library")
	transitionExistingLibraryKind(f, "kept", "kt_jvm_library")

	got := make(map[string]string)
	for _, r := range f.Rules {
		got[r.Name()] = r.Kind()
	}
	require.Equal(t, map[string]string{
		"kotlin": "kt_jvm_library",
		"kept":   "java_library",
	}, got)
}

func TestDeclaredOuterClassNames(t *testing.T) {
	declared := sorted_set.NewSortedSetFn([]types.ClassName{
		types.NewClassName(types.NewPackageName("com.example"), "Regular"),
		types.NewClassName(types.NewPackageName("com.example"), "lspe_config"),
	}, types.ClassNameLess)

	got := declaredOuterClassNames(declared).SortedSlice()
	want := []string{"Regular", "lspe_config"}
	require.Equal(t, want, got)
}

func TestAddNonLocalImports(t *testing.T) {
	src := sorted_set.NewSortedSetFn[types.ClassName]([]types.ClassName{}, types.ClassNameLess)
	for _, s := range []string{
		"com.example.a.b.Foo",        // same pkg, included class name: delete
		"com.example.a.b.Bar",        // same pkg, included class name: delete
		"com.example.a.b.Bar.SubBar", // same pkg, nested class, included class name: delete
		"com.example.a.b.Baz",        // same pkg, not included class name: keep
		"com.example.a.b.Baz.SubBaz", // same pkg, nested class, not included class name: keep
		"com.example.a.b.lspe_config.Builder",
		"com.example.a.b.c.Foo", // different pkg: keep
		"com.example.a.Foo",     // different pkg: keep
		"com.another.a.b.Foo",   // different pkg: keep
	} {
		name, err := types.ParseClassName(s)
		if err != nil {
			t.Fatal(err)
		}
		src.Add(*name)
	}

	depsDst := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	exportsDst := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	addNonLocalImportsAndExports(depsDst, nil, exportsDst, nil, src, sorted_set.NewSortedSetFn[types.PackageName]([]types.PackageName{}, types.PackageNameLess), sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess), types.NewPackageName("com.example.a.b"), sorted_set.NewSortedSet([]string{"Foo", "Bar", "lspe_config"}))

	want := stringsToPackageNames([]string{
		"com.another.a.b",
		"com.example.a",
		"com.example.a.b",
		"com.example.a.b.c",
	}).SortedSlice()

	if diff := cmp.Diff(want, depsDst.SortedSlice()); diff != "" {
		t.Errorf("filterImports() mismatch (-want +got):\n%s", diff)
	}
}

func TestGenerateJavaLibraryPreservesUnqualifiedUndeclaredSamePackageClass(t *testing.T) {
	sharedPackage := types.NewPackageName("com.example.shared")
	consumer := types.NewClassName(sharedPackage, "Consumer")
	localSibling := types.NewClassName(sharedPackage, "LocalSibling")
	provider := types.NewClassName(sharedPackage, "Provider")

	// Model parser output for Consumer referring to a local sibling and to an
	// unqualified Provider supplied by another workspace target in the same package.
	importedClasses := sorted_set.NewSortedSetFn([]types.ClassName{
		localSibling,
		provider,
	}, types.ClassNameLess)
	declaredClasses := sorted_set.NewSortedSetFn([]types.ClassName{
		consumer,
		localSibling,
	}, types.ClassNameLess)
	modulePackages := stringsToPackageNames([]string{"com.example.shared"})

	importedPackages, importedClasses := filterImportsInModule(
		stringsToPackageNames([]string{"com.example.shared"}),
		importedClasses,
		modulePackages,
		declaredClasses,
	)

	var res language.GenerateResult
	l := newTestJavaLang(t)
	l.generateJavaLibrary(generateJavaLibraryArgs{
		File:                    nil,
		Rel:                     "consumer",
		LibraryKind:             "java_library",
		Result:                  &res,
		Config:                  javaconfig.New("."),
		Name:                    "consumer",
		Srcs:                    []string{"consumer/Consumer.java", "consumer/LocalSibling.java"},
		Packages:                modulePackages,
		Imports:                 importedPackages,
		ImportedClasses:         importedClasses,
		Exports:                 stringsToPackageNames(nil),
		ExportedClasses:         nil,
		ExternalExportedClasses: nil,
		AnnotationProcessors:    nil,
		TestOnly:                false,
	})

	require.Len(t, res.Imports, 1)
	resolveInput := res.Imports[0].(types.ResolveInput)
	require.Empty(t, resolveInput.ImportedPackageNames.SortedSlice())
	require.Equal(
		t, []types.ClassName{provider}, resolveInput.ImportedClasses.SortedSlice(),
	)
}

func TestFilterNamespaceClassesInModule(t *testing.T) {
	modulePackages := stringsToPackageNames([]string{
		"com.example.mod.PaymentMethods",
		"com.example.mod.PaymentMethods.AvailableFilters",
	})
	declaredClasses := sorted_set.NewSortedSetFn([]types.ClassName{
		types.NewClassName(types.NewPackageName("com.example.mod.PaymentMethods.AvailableFilters"), "QueryEngine"),
		types.NewClassName(types.NewPackageName("com.example.mod.PaymentMethods"), "TypeAliasDefinitionsKt"),
	}, types.ClassNameLess)
	packages := stringsToPackageNames([]string{
		"com.example.mod",
		"com.external",
	})
	classes := sorted_set.NewSortedSetFn[types.ClassName]([]types.ClassName{}, types.ClassNameLess)
	for _, value := range []string{
		"com.example.mod.PaymentMethods",
		"com.example.mod.PaymentMethods.AvailableFilters.QueryEngine",
		"com.example.mod.PaymentMethods.TypeAliasDefinitionsKt.Companion",
		"com.example.mod.PaymentMethods.ExternalSplit",
		"com.external.Widget",
	} {
		class, err := types.ParseClassName(value)
		if err != nil {
			t.Fatal(err)
		}
		classes.Add(*class)
	}

	gotPackages, gotClasses :=
		filterNamespaceClassesInModule(packages, classes, modulePackages, declaredClasses)
	require.Equal(t, []types.PackageName{{Name: "com.external"}}, gotPackages.SortedSlice())

	var gotClassNames []string
	for _, class := range gotClasses.SortedSlice() {
		gotClassNames = append(gotClassNames, class.FullyQualifiedClassName())
	}
	require.Equal(t, []string{
		"com.example.mod.PaymentMethods.ExternalSplit",
		"com.external.Widget",
	}, gotClassNames)
}

func TestCollectResourceFilesRecursivelyStopsAtPackageBoundaries(t *testing.T) {
	dir := t.TempDir()
	for _, subdir := range []string{"resources/nested", "resources/package"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, subdir), 0o755))
	}
	for name, contents := range map[string]string{
		"resources/direct.txt":        "direct",
		"resources/nested/kept.txt":   "nested",
		"resources/package/BUILD":     "filegroup(name = \"resources\")",
		"resources/package/owned.txt": "owned",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644))
	}

	got := collectResourceFilesRecursively(language.GenerateArgs{Dir: dir}, "resources")
	require.ElementsMatch(t, []string{
		"resources/direct.txt",
		"resources/nested/kept.txt",
	}, got)
}

func TestResourceDirectoryOwnsFilesStopsAtPackageBoundaries(t *testing.T) {
	dir := t.TempDir()
	for _, subdir := range []string{"nested", "package"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, subdir), 0o755))
	}
	for name, contents := range map[string]string{
		"BUILD":                 "filegroup(name = \"resources\")",
		"nested/kept.txt":       "kept",
		"package/BUILD":         "filegroup(name = \"resources\")",
		"package/not-owned.txt": "not owned",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644))
	}

	require.True(t, resourceDirectoryOwnsFiles(dir))
	require.NoError(t, os.Remove(filepath.Join(dir, "nested/kept.txt")))
	require.False(t, resourceDirectoryOwnsFiles(dir))
	require.False(t, resourceDirectoryOwnsFiles(filepath.Join(dir, "missing")))
}

func TestResourceDirectoryOwnsFilesIgnoresJavaSources(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Only.java"), []byte("class Only {}"), 0o644))
	require.False(t, resourceDirectoryOwnsFiles(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owned.yaml"), []byte("owned"), 0o644))
	require.True(t, resourceDirectoryOwnsFiles(dir))
}

func TestIsOwnedByModuleRoot(t *testing.T) {
	root := javaconfig.New(".")
	require.NoError(t, root.SetModuleGranularity("module"))

	owned := root.NewChild()
	packageBoundary := root.NewChild()
	require.NoError(t, packageBoundary.SetModuleGranularity("package"))
	packageChild := packageBoundary.NewChild()
	nestedRoot := packageBoundary.NewChild()
	require.NoError(t, nestedRoot.SetModuleGranularity("module"))
	nestedChild := nestedRoot.NewChild()
	directNestedRoot := root.NewChild()
	require.NoError(t, directNestedRoot.SetModuleGranularity("module"))
	directNestedChild := directNestedRoot.NewChild()

	cfgs := javaconfig.Configs{
		"module":                   root,
		"module/owned":             owned,
		"module/separate":          packageBoundary,
		"module/separate/child":    packageChild,
		"module/separate/nested":   nestedRoot,
		"module/separate/nested/x": nestedChild,
		"module/direct-nested":     directNestedRoot,
		"module/direct-nested/x":   directNestedChild,
	}

	for name, tc := range map[string]struct {
		candidate string
		want      bool
	}{
		"root":                     {candidate: "module", want: true},
		"inherited module child":   {candidate: "module/owned", want: true},
		"package boundary":         {candidate: "module/separate", want: false},
		"package boundary child":   {candidate: "module/separate/child", want: false},
		"nested module root":       {candidate: "module/separate/nested", want: false},
		"nested module root child": {candidate: "module/separate/nested/x", want: false},
		"direct nested root":       {candidate: "module/direct-nested", want: false},
		"direct nested root child": {candidate: "module/direct-nested/x", want: false},
		"path prefix sibling":      {candidate: "module-other", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isOwnedByModuleRoot(cfgs, tc.candidate, "module"); got != tc.want {
				t.Fatalf("isOwnedByModuleRoot(%q, %q) = %t, want %t", tc.candidate, "module", got, tc.want)
			}
		})
	}
}

func newTestJavaLang(t *testing.T) javaLang {
	t.Helper()
	logger := zerolog.New(zerolog.NewTestWriter(t))
	return javaLang{
		logger:          logger,
		javaExportIndex: java_export_index.NewJavaExportIndex(languageName, logger),
	}
}

func stringsToPackageNames(strs []string) *sorted_set.SortedSet[types.PackageName] {
	ret := sorted_set.NewSortedSetFn[types.PackageName]([]types.PackageName{}, types.PackageNameLess)
	for _, s := range strs {
		ret.Add(types.NewPackageName(s))
	}
	return ret
}

func TestSnakeToPascalCase(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		want  string
	}{
		"empty":               {input: "", want: ""},
		"single_char":         {input: "a", want: "A"},
		"already_pascal":      {input: "Http", want: "Http"},
		"simple":              {input: "http", want: "Http"},
		"snake_case":          {input: "sawmill_raw_http_request", want: "SawmillRawHttpRequest"},
		"letter_after_digits": {input: "a2p_10dlc", want: "A2P10Dlc"},
		"digit_word_suffix":   {input: "authn_3ds", want: "Authn3Ds"},
		"existing_capitals":   {input: "already_HTTP", want: "AlreadyHTTP"},
		"other_separators":    {input: "dash-dot.name", want: "DashDotName"},
	} {
		t.Run(name, func(t *testing.T) {
			got := snakeToPascalCase(tc.input)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestProtoOuterClassName(t *testing.T) {
	for name, tc := range map[string]struct {
		fileInfo proto.FileInfo
		want     string
	}{
		"simple_file_name": {
			fileInfo: proto.FileInfo{Name: "http.proto"},
			want:     "Http",
		},
		"snake_case_file_name": {
			fileInfo: proto.FileInfo{Name: "sawmill_raw_http_request.proto"},
			want:     "SawmillRawHttpRequest",
		},
		"file_with_path": {
			fileInfo: proto.FileInfo{Name: "squareup/logging/http.proto"},
			want:     "Http",
		},
		"explicit_outer_classname": {
			fileInfo: proto.FileInfo{
				Name:    "http.proto",
				Options: []proto.Option{{Key: "java_outer_classname", Value: "HttpProtos"}},
			},
			want: "HttpProtos",
		},
		"explicit_outer_classname_overrides_filename": {
			fileInfo: proto.FileInfo{
				Name:    "some_other_name.proto",
				Options: []proto.Option{{Key: "java_outer_classname", Value: "CustomName"}},
			},
			want: "CustomName",
		},
		"default_outer_class_conflicts_with_service": {
			fileInfo: proto.FileInfo{Name: "cart_service.proto", Services: []string{"CartService"}},
			want:     "CartServiceOuterClass",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := protoOuterClassName(tc.fileInfo)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseProtoJavaLayout(t *testing.T) {
	content := []byte(`
    syntax = "proto2";
    option java_multiple_files = true;
    option java_generic_services = true;
    // message CommentedOut {}
    message TopLevel {
      message NestedMessage {}
      enum NestedEnum { UNKNOWN = 0; }
    }
    enum TopLevelEnum { UNKNOWN = 0; }
    service TopLevelService {}
  `)

	got := parseProtoJavaLayout(content)
	require.True(t, got.multipleFiles)
	require.True(t, got.genericServices)
	require.Equal(t, []string{"TopLevel"}, got.topLevelMessages)
	require.Equal(t, []string{"TopLevelEnum"}, got.topLevelEnums)
}

func TestParseProtoJavaLayoutDefaultsToSingleFile(t *testing.T) {
	content := []byte(`
    syntax = "proto2";
    message TopLevel {}
  `)

	got := parseProtoJavaLayout(content)
	require.False(t, got.multipleFiles)
	require.False(t, got.genericServices)
	require.Equal(t, []string{"TopLevel"}, got.topLevelMessages)
	require.Empty(t, got.topLevelEnums)
}

func TestGeneratedProtoClasses(t *testing.T) {
	packageName := types.NewPackageName("com.example")
	for name, tc := range map[string]struct {
		fileInfo proto.FileInfo
		layout   protoJavaLayout
		want     []string
	}{
		"single_file_with_generic_service": {
			fileInfo: proto.FileInfo{
				Name:     "service.proto",
				Services: []string{"SquareTokenService"},
			},
			layout: protoJavaLayout{
				genericServices:  true,
				topLevelMessages: []string{"Cart"},
			},
			want: []string{
				"com.example.Service",
				"com.example.Service.SquareTokenService",
				"com.example.SquareTokenServiceGrpc",
			},
		},
		"multiple_files_with_non_generic_service": {
			fileInfo: proto.FileInfo{
				Name:     "commerce.proto",
				Services: []string{"OrderService"},
			},
			layout: protoJavaLayout{
				multipleFiles:    true,
				topLevelMessages: []string{"Order"},
				topLevelEnums:    []string{"Status"},
			},
			want: []string{
				"com.example.Commerce",
				"com.example.Order",
				"com.example.OrderOrBuilder",
				"com.example.OrderServiceGrpc",
				"com.example.Status",
			},
		},
		"multiple_files_with_generic_service": {
			fileInfo: proto.FileInfo{
				Name:     "admin_service.proto",
				Services: []string{"AdminService"},
			},
			layout: protoJavaLayout{
				multipleFiles:   true,
				genericServices: true,
			},
			want: []string{
				"com.example.AdminService",
				"com.example.AdminServiceGrpc",
				"com.example.AdminServiceOuterClass",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			classes := generatedProtoClasses(tc.fileInfo, tc.layout, packageName)
			got := make([]string, 0, classes.Len())
			for _, class := range classes.SortedSlice() {
				got = append(got, class.FullyQualifiedClassName())
			}
			require.Equal(t, tc.want, got)
		})
	}
}
