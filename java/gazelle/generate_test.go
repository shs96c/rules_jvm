package gazelle

import (
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/sorted_set"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/language/proto"
	bzl "github.com/bazelbuild/buildtools/build"
	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

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

func TestAddNonLocalImports(t *testing.T) {
	src := sorted_set.NewSortedSetFn[types.ClassName]([]types.ClassName{}, types.ClassNameLess)
	for _, s := range []string{
		"com.example.a.b.Foo",        // same pkg, included class name: delete
		"com.example.a.b.Bar",        // same pkg, included class name: delete
		"com.example.a.b.Bar.SubBar", // same pkg, nested class, included class name: delete
		"com.example.a.b.Baz",        // same pkg, not included class name: keep
		"com.example.a.b.Baz.SubBaz", // same pkg, nested class, not included class name: keep
		"com.example.a.b.c.Foo",      // different pkg: keep
		"com.example.a.Foo",          // different pkg: keep
		"com.another.a.b.Foo",        // different pkg: keep
	} {
		name, err := types.ParseClassName(s)
		if err != nil {
			t.Fatal(err)
		}
		src.Add(*name)
	}

	depsDst := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	exportsDst := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	addNonLocalImportsAndExports(depsDst, nil, exportsDst, nil, src, sorted_set.NewSortedSetFn[types.PackageName]([]types.PackageName{}, types.PackageNameLess), sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess), types.NewPackageName("com.example.a.b"), sorted_set.NewSortedSet([]string{"Foo", "Bar"}))

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
	return javaLang{
		logger: zerolog.New(zerolog.NewTestWriter(t)),
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
