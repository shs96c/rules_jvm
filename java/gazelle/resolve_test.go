package gazelle

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/maven"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/sorted_set"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/language/proto"
	"github.com/bazelbuild/bazel-gazelle/pathtools"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/bazel-gazelle/testtools"
	"github.com/bazelbuild/bazel-gazelle/walk"
	bzl "github.com/bazelbuild/buildtools/build"
	"github.com/sergi/go-diff/diffmatchpatch"
	"golang.org/x/tools/go/vcs"
)

func TestSetLabelAttrIncludingExistingValuesPreservesGeneratedAndInferredLabels(t *testing.T) {
	r := rule.NewRule("java_library", "app")
	r.SetAttr("deps", []string{":proto"})
	labels := sorted_set.NewSortedSetFn(
		[]label.Label{label.New("maven", "", "joda_time_joda_time")},
		sorted_set.LabelLess,
	)

	setLabelAttrIncludingExistingValues(r, "deps", labels)

	want := []string{":proto", "@maven//:joda_time_joda_time"}
	if got := r.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestSetManagedLabelAttrReplacesASTExpression(t *testing.T) {
	r := rule.NewRule("java_library", "app")
	oldExpr := &bzl.ListExpr{List: []bzl.Expr{
		&bzl.StringExpr{Value: "@maven//:org_jruby_jruby_complete"},
	}}
	r.SetAttr("deps", oldExpr)
	labels := sorted_set.NewSortedSetFn(
		[]label.Label{label.New("maven", "", "joda_time_joda_time")},
		sorted_set.LabelLess,
	)

	setManagedLabelAttr(r, "deps", labels)

	if got := r.Attr("deps"); got == oldExpr {
		t.Fatal("deps still references the destination AST expression")
	}
	want := []string{"@maven//:joda_time_joda_time"}
	if got := r.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestSetLabelAttrIncludingExistingValuesPreservesPlugins(t *testing.T) {
	r := rule.NewRule("java_library", "app")
	r.SetAttr("plugins", []string{"//processors:manual"})
	labels := sorted_set.NewSortedSetFn(
		[]label.Label{label.New("", "processors", "inferred")},
		sorted_set.LabelLess,
	)

	setLabelAttrIncludingExistingValues(r, "plugins", labels)

	want := []string{
		"//processors:inferred",
		"//processors:manual",
	}
	if got := r.AttrStrings("plugins"); !reflect.DeepEqual(got, want) {
		t.Errorf("plugins mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveSingleClassPrefersSelfCandidate(t *testing.T) {
	javaPackage := types.NewPackageName("com.example.duplicate")
	className := types.NewClassName(javaPackage, "Duplicate")
	from := label.New("java", "consumer", "app")
	other := label.New("java", "provider_b", "lib")

	for name, self := range map[string]label.Label{
		"explicit current repository spelling": label.New("java", "consumer", "app"),
		"absolute workspace spelling":          label.New("", "consumer", "app"),
		"relative spelling":                    {Name: "app", Relative: true},
	} {
		t.Run(name, func(t *testing.T) {
			lang := newTestJavaLang(t)
			resolver := NewResolver(&lang)
			resolver.classIndex[javaPackage] = &packageClassIndex{
				prod: map[string][]label.Label{
					"Duplicate": {other, self},
				},
				test: make(map[string][]label.Label),
			}

			consumer := rule.NewRule("java_library", "app")
			consumer.SetAttr("deps", []string{"@java//provider_b:lib"})
			c, _, _ := testConfig(t)
			c.RepoName = "java"
			pc := javaconfig.New(".")
			pc.SetResolveToJavaExports(false)
			preferences := collectExistingLabelPreferences(consumer, "deps", c.RepoName, from)

			got := resolver.resolveSingleClass(
				c,
				pc,
				className,
				nil,
				from,
				false,
				preferences,
			)
			want := simplifyLabel(c.RepoName, self, from)
			if got != want {
				t.Errorf("resolveSingleClass() = %s, want self candidate %s", got, want)
			}
		})
	}
}

func TestResolveKotlinReflectionFunctionClass(t *testing.T) {
	config := javaconfig.New(".")
	config.SetMavenRepositoryName("custom_maven")
	wantReflect := maven.LabelFromArtifact("custom_maven", "org.jetbrains.kotlin:kotlin-reflect")

	for name, tc := range map[string]struct {
		className string
		want      label.Label
	}{
		"zero arity": {
			className: "kotlin.reflect.KFunction0",
			want:      wantReflect,
		},
		"multi-digit arity": {
			className: "kotlin.reflect.KFunction123",
			want:      wantReflect,
		},
		"missing arity": {
			className: "kotlin.reflect.KFunction",
			want:      label.NoLabel,
		},
		"plural name": {
			className: "kotlin.reflect.KFunctions",
			want:      label.NoLabel,
		},
		"suffix after arity": {
			className: "kotlin.reflect.KFunction3Suffix",
			want:      label.NoLabel,
		},
		"nested suffix after arity": {
			className: "kotlin.reflect.KFunction3.Inner",
			want:      label.NoLabel,
		},
		"non-ASCII digit": {
			className: "kotlin.reflect.KFunction٣",
			want:      label.NoLabel,
		},
		"other reflection type": {
			className: "kotlin.reflect.KProperty0",
			want:      label.NoLabel,
		},
		"nested reflection package": {
			className: "kotlin.reflect.full.KFunction3",
			want:      label.NoLabel,
		},
	} {
		t.Run(name, func(t *testing.T) {
			className, err := types.ParseClassName(tc.className)
			if err != nil {
				t.Fatal(err)
			}
			if got := resolveKotlinReflectionFunctionClass(config, *className); got != tc.want {
				t.Errorf("resolveKotlinReflectionFunctionClass(%q) = %s, want %s", tc.className, got, tc.want)
			}
		})
	}
}

func TestResolveSingleClassPrefersConsumerExistingCandidate(t *testing.T) {
	javaPackage := types.NewPackageName("com.example.duplicate")
	className := types.NewClassName(javaPackage, "Duplicate")
	from := label.New("java", "consumer", "app")
	providerA := label.New("", "consumer", "provider_a")
	providerB := label.New("java", "provider_b", "lib")

	for name, tc := range map[string]struct {
		existing []string
		want     label.Label
	}{
		"relative native edge selects candidate": {
			existing: []string{":provider_a"},
			want:     label.Label{Name: "provider_a", Relative: true},
		},
		"explicit current repository edge selects candidate": {
			existing: []string{"@java//provider_b:lib"},
			want:     label.New("", "provider_b", "lib"),
		},
		"no existing edge remains ambiguous": {
			want: label.NoLabel,
		},
		"stale non-candidate edge remains ambiguous": {
			existing: []string{"//stale:lib"},
			want:     label.NoLabel,
		},
		"multiple matching existing edges remain ambiguous": {
			existing: []string{":provider_a", "//provider_b:lib"},
			want:     label.NoLabel,
		},
	} {
		t.Run(name, func(t *testing.T) {
			lang := newTestJavaLang(t)
			resolver := NewResolver(&lang)
			resolver.classIndex[javaPackage] = &packageClassIndex{
				prod: map[string][]label.Label{
					"Duplicate": {providerA, providerB},
				},
				test: make(map[string][]label.Label),
			}

			consumer := rule.NewRule("java_library", "app")
			if len(tc.existing) > 0 {
				consumer.SetAttr("deps", tc.existing)
			}
			c, _, _ := testConfig(t)
			c.RepoName = "java"
			pc := javaconfig.New(".")
			pc.SetResolveToJavaExports(false)
			preferences := collectExistingLabelPreferences(consumer, "deps", c.RepoName, from)

			got := resolver.resolveSingleClass(
				c,
				pc,
				className,
				nil,
				from,
				false,
				preferences,
			)
			if got.String() != tc.want.String() {
				t.Errorf("resolveSingleClass() = %s, want %s", got, tc.want)
			}
		})
	}
}

// splitOwnerPackageMavenResolver reports a package owned by multiple Maven
// artifacts whose classes are absent from the class index, mirroring published
// artifacts that bundle the same generated code.
type splitOwnerPackageMavenResolver struct{}

func (*splitOwnerPackageMavenResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	if pkg.Name == "com.example.generated" {
		return label.NoLabel, &maven.MultipleExternalImportsError{
			PackageName: pkg.Name,
			PossiblePackages: []string{
				"@maven//:com_example_client",
				"@maven//:com_example_embedded",
			},
		}
	}
	return label.NoLabel, &maven.NoExternalImportsError{PackageName: pkg.Name}
}

func (*splitOwnerPackageMavenResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, nil
}

// TestResolveSingleClassKeepsExistingMavenOwnerOverCrossResolver covers a class
// with no in-repo provider whose package is owned by an existing Maven
// dependency: a cross-resolver candidate the rule has never compiled against
// must not replace that owner, which would duplicate the package's classes
// across two classpath jars.
func TestResolveSingleClassKeepsExistingMavenOwnerOverCrossResolver(t *testing.T) {
	javaPackage := types.NewPackageName("com.example.generated")
	className := types.NewClassName(javaPackage, "GeneratedMessage")
	from := label.New("", "consumer", "app")
	crossLabel := label.New("com_example_protos", "gen", "generated_java_proto")

	for name, tc := range map[string]struct {
		existing []string
		want     label.Label
	}{
		"existing maven owner outranks cross-resolver candidate": {
			existing: []string{"@maven//:com_example_client"},
			want:     label.New("maven", "", "com_example_client"),
		},
		"cross-resolver candidate kept when already a dependency": {
			existing: []string{"@com_example_protos//gen:generated_java_proto"},
			want:     crossLabel,
		},
		"cross-resolver candidate kept without existing owner": {
			want: crossLabel,
		},
		"multiple existing owners keep cross-resolver candidate": {
			existing: []string{"@maven//:com_example_client", "@maven//:com_example_embedded"},
			want:     crossLabel,
		},
	} {
		t.Run(name, func(t *testing.T) {
			lang := newTestJavaLang(t)
			lang.mavenResolver = &splitOwnerPackageMavenResolver{}
			resolver := NewResolver(&lang)

			ix := resolve.NewRuleIndex(nil, &fakeClassCrossResolver{
				classToLabel: map[string]label.Label{
					"com.example.generated.GeneratedMessage": crossLabel,
				},
			})
			ix.Finish()

			consumer := rule.NewRule("java_library", "app")
			if len(tc.existing) > 0 {
				consumer.SetAttr("deps", tc.existing)
			}
			c, _, _ := testConfig(t)
			pc := javaconfig.New(".")
			pc.SetResolveToJavaExports(false)
			preferences := collectExistingLabelPreferences(consumer, "deps", c.RepoName, from)

			got := resolver.resolveSingleClass(c, pc, className, ix, from, false, preferences)
			if got.String() != tc.want.String() {
				t.Errorf("resolveSingleClass() = %s, want %s", got, tc.want)
			}
		})
	}
}

// singleOwnerPackageMavenResolver reports a package owned by exactly one Maven
// artifact whose classes are absent from the class index, mirroring a production
// lockfile, which carries no class-level data.
type singleOwnerPackageMavenResolver struct{}

func (*singleOwnerPackageMavenResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	if pkg.Name == "com.example.generated" {
		return label.New("maven", "", "com_example_client"), nil
	}
	return label.NoLabel, &maven.NoExternalImportsError{PackageName: pkg.Name}
}

func (*singleOwnerPackageMavenResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, nil
}

// TestPopulateAttrKeepsExistingMavenPackageOwnerOverCrossResolver covers a
// package that resolves unambiguously to a Maven dependency the rule already
// compiles against, while a cross-resolver also claims its individual classes
// (the Maven owner never appears in the class-export cache, so every class
// takes the per-class fallback): the generated-code candidate must not replace
// the existing owner. Existing dependencies reach resolution through
// ResolveInput, mirroring the production flow where generated rules never
// carry the checked-in attributes.
func TestPopulateAttrKeepsExistingMavenPackageOwnerOverCrossResolver(t *testing.T) {
	javaPackage := types.NewPackageName("com.example.generated")
	className := types.NewClassName(javaPackage, "GeneratedMessage")
	from := label.New("", "consumer", "app")
	crossLabel := label.New("com_example_protos", "gen", "generated_java_proto")

	for name, tc := range map[string]struct {
		existing []string
		want     []string
	}{
		"existing maven owner survives cross-resolver class claim": {
			existing: []string{"@maven//:com_example_client"},
			want:     []string{"@maven//:com_example_client"},
		},
		"cross-resolver candidate kept when already a dependency": {
			existing: []string{"@com_example_protos//gen:generated_java_proto"},
			want:     []string{"@com_example_protos//gen:generated_java_proto"},
		},
		"cross-resolver candidate wins for a new import": {
			want: []string{"@com_example_protos//gen:generated_java_proto"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			lang := newTestJavaLang(t)
			lang.mavenResolver = &singleOwnerPackageMavenResolver{}
			resolver := NewResolver(&lang)

			ix := resolve.NewRuleIndex(nil, &fakeClassCrossResolver{
				classToLabel: map[string]label.Label{
					"com.example.generated.GeneratedMessage": crossLabel,
				},
			})
			ix.Finish()

			consumer := rule.NewRule("java_library", "app")
			consumer.SetAttr("srcs", []string{"App.java"})
			existingDeps := sorted_set.NewSortedSetFn([]label.Label{}, sorted_set.LabelLess)
			for _, raw := range tc.existing {
				l, err := label.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				existingDeps.Add(l)
			}
			c, _, _ := testConfig(t)
			pc := javaconfig.New(".")
			pc.SetResolveToJavaExports(false)

			requiredPackages := sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess)
			importedClasses := sorted_set.NewSortedSetFn([]types.ClassName{className}, types.ClassNameLess)

			resolver.populateAttr(c, pc, consumer, "deps", requiredPackages, importedClasses, ix, false, from, nil, existingDeps)

			if got := consumer.AttrStrings("deps"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("populateAttr() deps = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPackageResolutionKeepsExistingMavenOwnerOverCrossResolver covers the
// package-level variant of the guard: an external plugin claims the whole
// package through CrossResolve, so resolution never reaches the class-level
// paths. Index lookups run before the Maven resolver, so without the guard
// the plugin's label silently replaces the checked-in Maven dependency.
func TestPackageResolutionKeepsExistingMavenOwnerOverCrossResolver(t *testing.T) {
	javaPackage := types.NewPackageName("com.example.generated")
	className := types.NewClassName(javaPackage, "GeneratedMessage")
	from := label.New("", "consumer", "app")
	crossLabel := label.New("com_example_protos", "gen", "generated_java_proto")
	inRepoLabel := label.New("", "newhome", "lib")

	for name, tc := range map[string]struct {
		packageOwner label.Label
		existing     []string
		want         []string
	}{
		"existing maven owner survives cross-resolver package claim": {
			packageOwner: crossLabel,
			existing:     []string{"@maven//:com_example_client"},
			want:         []string{"@maven//:com_example_client"},
		},
		"cross-resolver candidate kept when already a dependency": {
			packageOwner: crossLabel,
			existing:     []string{"@com_example_protos//gen:generated_java_proto"},
			want:         []string{"@com_example_protos//gen:generated_java_proto"},
		},
		"cross-resolver candidate wins for a new import": {
			packageOwner: crossLabel,
			want:         []string{"@com_example_protos//gen:generated_java_proto"},
		},
		"in-repo provider outranks an existing maven dependency": {
			packageOwner: inRepoLabel,
			existing:     []string{"@maven//:com_example_client"},
			want:         []string{"//newhome:lib"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			lang := newTestJavaLang(t)
			lang.mavenResolver = &singleOwnerPackageMavenResolver{}
			resolver := NewResolver(&lang)

			ix := resolve.NewRuleIndex(nil, &fakeClassCrossResolver{
				classToLabel: map[string]label.Label{
					"com.example.generated": tc.packageOwner,
				},
			})
			ix.Finish()

			consumer := rule.NewRule("java_library", "app")
			consumer.SetAttr("srcs", []string{"App.java"})
			existingDeps := sorted_set.NewSortedSetFn([]label.Label{}, sorted_set.LabelLess)
			for _, raw := range tc.existing {
				l, err := label.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				existingDeps.Add(l)
			}
			c, _, _ := testConfig(t)
			pc := javaconfig.New(".")
			pc.SetResolveToJavaExports(false)

			requiredPackages := sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess)
			importedClasses := sorted_set.NewSortedSetFn([]types.ClassName{className}, types.ClassNameLess)

			resolver.populateAttr(c, pc, consumer, "deps", requiredPackages, importedClasses, ix, false, from, nil, existingDeps)

			if got := consumer.AttrStrings("deps"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("populateAttr() deps = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImports(t *testing.T) {
	type buildFile struct {
		rel, content string
	}

	type wantImport struct {
		importSpec resolve.ImportSpec
		labelName  string
	}

	type testCase struct {
		old  buildFile
		want []wantImport
	}

	for name, tc := range map[string]testCase{
		"java": {
			old: buildFile{
				rel: "",
				content: `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "hello",
    srcs = ["Hello.java"],
	_packages = ["com.example"],
    visibility = ["//:__subpackages__"],
)`,
			},
			want: []wantImport{
				wantImport{
					importSpec: resolve.ImportSpec{
						Lang: "java",
						Imp:  "com.example",
					},
					labelName: "hello",
				},
			},
		},
		"kotlin": {
			old: buildFile{
				rel: "",
				content: `load("@rules_kotlin//kotlin:jvm.bzl", "kt_jvm_library")

# gazelle:jvm_kotlin_enabled true

kt_jvm_library(
    name = "hello",
    srcs = ["Hello.kt"],
	_packages = ["com.example"],
    visibility = ["//:__subpackages__"],
)`,
			},
			want: []wantImport{
				wantImport{
					importSpec: resolve.ImportSpec{
						Lang: "java",
						Imp:  "com.example",
					},
					labelName: "hello",
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, langs, confs := testConfig(t)

			mrslv, exts := InitTestResolversAndExtensions(langs)
			ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)

			buildPath := filepath.Join(filepath.FromSlash(tc.old.rel), "BUILD.bazel")
			f, err := rule.LoadData(buildPath, tc.old.rel, []byte(tc.old.content))
			if err != nil {
				t.Fatal(err)
			}
			for _, configurer := range confs {
				// Update the config to handle gazelle directives in the BUILD file.
				configurer.Configure(c, tc.old.rel, f)
			}
			for _, r := range f.Rules {
				// Explicitly set the private `_java_packages` attribute for import resolution,
				// This must be done manually as all the attributes stated in the BUILD file are
				// considered public.
				setPackagesPrivateAttr(r)
				ix.AddRule(c, r, f)
				t.Logf("added rule %s", r.Name())
			}
			ix.Finish()

			for _, want := range tc.want {
				results := ix.FindRulesByImportWithConfig(c, want.importSpec, "java")
				if len(results) != 1 {
					t.Errorf("expected 1 result, got %d for import %v", len(results), want.importSpec.Imp)
				} else {
					if results[0].Label.Name != want.labelName {
						t.Errorf("expected label %s, got %s", want.labelName, results[0].Label)
					}
				}
			}
		})
	}
}

func TestResolve(t *testing.T) {
	type buildFile struct {
		rel, content string
	}

	type testCase struct {
		old  buildFile
		want string
	}

	for name, tc := range map[string]testCase{
		"internal": {
			old: buildFile{
				rel: "",
				content: `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "hello",
    srcs = ["Hello.java"],
    _imported_packages = ["java.lang"],
    _packages = ["com.example"],
    visibility = ["//:__subpackages__"],
)`,
			},
			want: `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "hello",
    srcs = ["Hello.java"],
    visibility = ["//:__subpackages__"],
)`,
		},
		"external": {
			old: buildFile{
				rel: "",
				content: `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "myproject",
    srcs = ["App.java"],
    _imported_packages = [
        "com.google.common.primitives",
        "java.lang",
    ],
    _packages = ["com.example"],
    visibility = ["//:__subpackages__"],
)`,
			},
			want: `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "myproject",
    srcs = ["App.java"],
    visibility = ["//:__subpackages__"],
    deps = ["@maven//:com_google_guava_guava"],
)`,
		},
		"java-kotlin-stdlib": {
			old: buildFile{
				rel: "",
				content: `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "myproject",
    srcs = ["App.java"],
    _imported_packages = ["kotlin.random"],
    _packages = ["com.example"],
    visibility = ["//:__subpackages__"],
)`,
			},
			want: `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "myproject",
    srcs = ["App.java"],
    visibility = ["//:__subpackages__"],
    deps = ["@maven//:org_jetbrains_kotlin_kotlin_stdlib"],
)`,
		},
		"kotlin": {
			old: buildFile{
				rel: "",
				content: `load("@rules_kotlin//kotlin:jvm.bzl", "kt_jvm_library")

# gazelle:jvm_kotlin_enabled true

kt_jvm_library(
    name = "myproject",
    srcs = ["App.kt"],
    _imported_packages = [
        "com.google.common.primitives",
        "kotlin.collections",
    ],
    _packages = ["com.example"],
    visibility = ["//:__subpackages__"],
)`,
			},
			want: `load("@rules_kotlin//kotlin:jvm.bzl", "kt_jvm_library")

# gazelle:jvm_kotlin_enabled true

kt_jvm_library(
    name = "myproject",
    srcs = ["App.kt"],
    visibility = ["//:__subpackages__"],
    deps = ["@maven//:com_google_guava_guava"],
)`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, langs, _ := testConfig(t)

			mrslv, exts := InitTestResolversAndExtensions(langs)
			ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
			rc := testRemoteCache(nil)

			buildPath := filepath.Join(filepath.FromSlash(tc.old.rel), "BUILD.bazel")
			f, err := rule.LoadData(buildPath, tc.old.rel, []byte(tc.old.content))
			if err != nil {
				t.Fatal(err)
			}
			imports := make([]interface{}, len(f.Rules))
			for i, r := range f.Rules {
				imports[i] = convertImportsAttr(r)
				ix.AddRule(c, r, f)
			}
			ix.Finish()
			for i, r := range f.Rules {
				mrslv.Resolver(r, "").Resolve(c, ix, rc, r, imports[i], label.New("", tc.old.rel, r.Name()))

				if r.Attr("deps") != nil {
					for _, dep := range r.Attr("deps").(*bzl.ListExpr).List {
						if _, ok := dep.(*bzl.StringExpr); !ok {
							t.Errorf("rule %s deps deps should have type []StringExpr", r.Name())
						}
					}
				}
			}
			f.Sync()
			got := strings.TrimSpace(string(bzl.Format(f.File)))
			want := strings.TrimSpace(tc.want)
			if got != want {
				dmp := diffmatchpatch.New()
				diffs := dmp.DiffMain(want, got, true)
				t.Errorf("Resolve:\n%s", dmp.DiffPrettyText(diffs))
			}
		})
	}
}

func testRemoteCache(knownRepos []repo.Repo) *repo.RemoteCache {
	rc, _ := repo.NewRemoteCache(knownRepos)
	rc.RepoRootForImportPath = stubRepoRootForImportPath
	rc.HeadCmd = func(_, _ string) (string, error) {
		return "", fmt.Errorf("HeadCmd not supported in test")
	}
	rc.ModInfo = stubModInfo
	return rc
}

// stubRepoRootForImportPath is a stub implementation of vcs.RepoRootForImportPath
func stubRepoRootForImportPath(importPath string, verbose bool) (*vcs.RepoRoot, error) {
	if pathtools.HasPrefix(importPath, "example.com/repo.git") {
		return &vcs.RepoRoot{
			VCS:  vcs.ByCmd("git"),
			Repo: "https://example.com/repo.git",
			Root: "example.com/repo.git",
		}, nil
	}

	if pathtools.HasPrefix(importPath, "example.com/repo") {
		return &vcs.RepoRoot{
			VCS:  vcs.ByCmd("git"),
			Repo: "https://example.com/repo.git",
			Root: "example.com/repo",
		}, nil
	}

	if pathtools.HasPrefix(importPath, "example.com") {
		return &vcs.RepoRoot{
			VCS:  vcs.ByCmd("git"),
			Repo: "https://example.com",
			Root: "example.com",
		}, nil
	}

	return nil, fmt.Errorf("could not resolve import path: %q", importPath)
}

// stubModInfo is a stub implementation of RemoteCache.ModInfo.
func stubModInfo(importPath string) (string, error) {
	if pathtools.HasPrefix(importPath, "example.com/repo/v2") {
		return "example.com/repo/v2", nil
	}
	if pathtools.HasPrefix(importPath, "example.com/repo") {
		return "example.com/repo", nil
	}
	return "", fmt.Errorf("could not find module for import path: %q", importPath)
}

func setPackagesPrivateAttr(r *rule.Rule) {
	packages := r.AttrStrings("_packages")
	resolvablePackages := make([]types.ResolvableJavaPackage, 0, len(packages))
	for _, pkg := range packages {
		pkgName := types.NewPackageName(pkg)
		resolvablePackages = append(resolvablePackages, *types.NewResolvableJavaPackage(pkgName, false, false))
	}
	r.SetPrivateAttr(packagesKey, resolvablePackages)
}

func convertImportsAttr(r *rule.Rule) types.ResolveInput {
	return types.ResolveInput{
		PackageNames:         packageAttrToSortedSet(r, "_packages"),
		ImportedPackageNames: packageAttrToSortedSet(r, "_imported_packages"),
	}
}

func packageAttrToSortedSet(r *rule.Rule, name string) *sorted_set.SortedSet[types.PackageName] {
	attrValues := r.AttrStrings(name)
	r.DelAttr(name)
	packages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	for _, v := range attrValues {
		packages.Add(types.NewPackageName(v))
	}
	return packages
}

func testConfig(t *testing.T, args ...string) (*config.Config, []language.Language, []config.Configurer) {
	// Add a -repo_root argument if none is present. Without this,
	// config.CommonConfigurer will try to auto-detect a WORKSPACE file,
	// which will fail.
	haveRoot := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "-repo_root") {
			haveRoot = true
			break
		}
	}
	if !haveRoot {
		args = append(args, "-repo_root=.")
	}

	cexts := []config.Configurer{
		new(config.CommonConfigurer),
		new(walk.Configurer),
		new(resolve.Configurer),
	}

	l := NewLanguage()
	l.(*javaLang).mavenResolver = &testResolver{}

	langs := []language.Language{
		proto.NewLanguage(),
		l,
	}

	c := testtools.NewTestConfig(t, cexts, langs, args)

	absRepoRoot, err := filepath.Abs(c.RepoRoot)
	if err != nil {
		t.Fatalf("error getting absolute path for workspace")
	}
	c.RepoRoot = absRepoRoot

	for _, lang := range langs {
		cexts = append(cexts, lang)
	}

	return c, langs, cexts
}

type testResolver struct{}

func (*testResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, errors.New("not implemented")
}

func (*testResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, errors.New("not implemented")
}

type mapResolver map[string]resolve.Resolver

func (mr mapResolver) Resolver(r *rule.Rule, f string) resolve.Resolver {
	return mr[r.Kind()]
}

func InitTestResolversAndExtensions(langs []language.Language) (mapResolver, []interface{}) {
	mrslv := make(mapResolver)
	exts := make([]interface{}, 0, len(langs))
	for _, lang := range langs {
		// TODO There has to be a better way to make this generic.
		if jLang, ok := lang.(*javaLang); ok {
			jLang.mavenResolver = NewTestMavenResolver()
			jLang.javaExportIndex.FinalizeIndex()
		}

		for kind := range lang.Kinds() {
			mrslv[kind] = lang
		}
		exts = append(exts, lang)
	}
	return mrslv, exts
}

type TestMavenResolver struct {
	data map[types.PackageName]label.Label
}

func NewTestMavenResolver() *TestMavenResolver {
	return &TestMavenResolver{
		data: map[types.PackageName]label.Label{
			types.NewPackageName("com.google.common.primitives"): label.New("maven", "", "com_google_guava_guava"),
			types.NewPackageName("kotlin.random"):                label.New("maven", "", "org_jetbrains_kotlin_kotlin_stdlib"),
			types.NewPackageName("org.junit"):                    label.New("maven", "", "junit_junit"),
		},
	}
}

func (r *TestMavenResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	l, found := r.data[pkg]
	if !found {
		return label.NoLabel, fmt.Errorf("unexpected import: %s", pkg)
	}
	return l, nil
}

func (r *TestMavenResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, nil
}

type externalGrpcMavenResolver struct{}

func (*externalGrpcMavenResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	if pkg.Name == "io.grpc" {
		return label.New(mavenRepositoryName, "", "io_grpc_grpc_api"), nil
	}
	return label.NoLabel, &maven.NoExternalImportsError{PackageName: pkg.Name}
}

func (*externalGrpcMavenResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	if className.FullyQualifiedClassName() == "io.grpc.ServerInterceptor" {
		return label.New(mavenRepositoryName, "", "io_grpc_grpc_api"), nil
	}
	return label.NoLabel, nil
}

func TestResolveMavenWholePackageClass(t *testing.T) {
	lang := &javaLang{mavenResolver: NewTestMavenResolver()}
	resolver := NewResolver(lang)
	config := javaconfig.New(".")
	className, err := types.ParseClassName("com.google.common.primitives.Ints")
	if err != nil {
		t.Fatal(err)
	}

	got, err := resolver.resolveMavenWholePackageClass(config, *className)
	if err != nil {
		t.Fatal(err)
	}
	want := label.New("maven", "", "com_google_guava_guava")
	if got != want {
		t.Fatalf("resolveMavenWholePackageClass() = %s, want %s", got, want)
	}
}

func TestResolveMavenWholePackageClassPrefersKotlinReflectionFunction(t *testing.T) {
	lang := &javaLang{mavenResolver: NewTestMavenResolver()}
	resolver := NewResolver(lang)
	config := javaconfig.New(".")
	className, err := types.ParseClassName("kotlin.reflect.KFunction3")
	if err != nil {
		t.Fatal(err)
	}

	got, err := resolver.resolveMavenWholePackageClass(config, *className)
	if err != nil {
		t.Fatal(err)
	}
	want := maven.LabelFromArtifact("maven", "org.jetbrains.kotlin:kotlin-reflect")
	if got != want {
		t.Fatalf("resolveMavenWholePackageClass() = %s, want %s", got, want)
	}
}

func TestProtoSplitPackageClassResolution(t *testing.T) {
	c, langs, _ := testConfig(t)

	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)

	pkg := "protos/logging"
	javaPackage := types.NewPackageName("com.example.protos.logging.http")

	httpLibContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "http_java_library",
    _packages = ["com.example.protos.logging.http"],
    exports = [":http_java_proto"],
)
`
	sawmillLibContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "sawmill_raw_http_request_java_library",
    _packages = ["com.example.protos.logging.http"],
    exports = [":sawmill_raw_http_request_java_proto"],
)
`

	buildPath := filepath.Join(filepath.FromSlash(pkg), "BUILD.bazel")

	httpFile, err := rule.LoadData(buildPath, pkg, []byte(httpLibContent))
	if err != nil {
		t.Fatal(err)
	}

	sawmillFile, err := rule.LoadData(buildPath, pkg, []byte(sawmillLibContent))
	if err != nil {
		t.Fatal(err)
	}

	var javaLangInstance *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			javaLangInstance = jl
			break
		}
	}
	if javaLangInstance == nil {
		t.Fatal("javaLang not found in langs")
	}

	httpRule := httpFile.Rules[0]
	setPackagesPrivateAttr(httpRule)
	httpLabel := label.New("", pkg, "http_java_library")
	javaLangInstance.classExportCache[httpLabel.String()] = classExportInfo{
		classes: []types.ClassName{
			types.NewClassName(javaPackage, "Http"),
			types.NewClassName(javaPackage, "Request"),
		},
		testonly: false,
	}
	ix.AddRule(c, httpRule, httpFile)

	sawmillRule := sawmillFile.Rules[0]
	setPackagesPrivateAttr(sawmillRule)
	sawmillLabel := label.New("", pkg, "sawmill_raw_http_request_java_library")
	javaLangInstance.classExportCache[sawmillLabel.String()] = classExportInfo{
		classes: []types.ClassName{
			types.NewClassName(javaPackage, "SawmillRawHttpRequest"),
		},
		testonly: false,
	}
	ix.AddRule(c, sawmillRule, sawmillFile)

	ix.Finish()

	importSpec := resolve.ImportSpec{Lang: "java", Imp: javaPackage.Name}
	matches := ix.FindRulesByImportWithConfig(c, importSpec, "java")
	if len(matches) != 2 {
		t.Fatalf("expected 2 providers for the package, got %d", len(matches))
	}

	resolver := NewResolver(javaLangInstance)

	pci := resolver.buildPackageClassIndex(c, javaPackage, ix)

	if _, ok := pci.prod["Http"]; !ok {
		t.Error("Http class should be indexed")
	} else if len(pci.prod["Http"]) != 1 {
		t.Errorf("Http should have exactly 1 provider, got %d", len(pci.prod["Http"]))
	} else if pci.prod["Http"][0] != httpLabel {
		t.Errorf("Http should be provided by http_java_library, got %s", pci.prod["Http"][0])
	}

	if _, ok := pci.prod["Request"]; !ok {
		t.Error("Request class should be indexed")
	} else if len(pci.prod["Request"]) != 1 {
		t.Errorf("Request should have exactly 1 provider, got %d", len(pci.prod["Request"]))
	} else if pci.prod["Request"][0] != httpLabel {
		t.Errorf("Request should be provided by http_java_library, got %s", pci.prod["Request"][0])
	}

	if _, ok := pci.prod["SawmillRawHttpRequest"]; !ok {
		t.Error("SawmillRawHttpRequest class should be indexed")
	} else if len(pci.prod["SawmillRawHttpRequest"]) != 1 {
		t.Errorf("SawmillRawHttpRequest should have exactly 1 provider, got %d", len(pci.prod["SawmillRawHttpRequest"]))
	} else if pci.prod["SawmillRawHttpRequest"][0] != sawmillLabel {
		t.Errorf("SawmillRawHttpRequest should be provided by sawmill_raw_http_request_java_library, got %s", pci.prod["SawmillRawHttpRequest"][0])
	}
}

func TestPackageClassIndexUsesTestSuiteHelperLibrary(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)

	const pkg = "tests/helpers"
	const suiteName = "helpers-tests"
	javaPackage := types.NewPackageName("com.example.testing")
	buildPath := filepath.Join(filepath.FromSlash(pkg), "BUILD.bazel")
	file, err := rule.LoadData(buildPath, pkg, []byte(`java_test_suite(name = "helpers-tests")`))
	if err != nil {
		t.Fatal(err)
	}
	suiteRule := file.Rules[0]
	suiteRule.SetPrivateAttr(packagesKey, []types.ResolvableJavaPackage{
		*types.NewResolvableJavaPackage(javaPackage, true, true),
	})

	var javaLangInstance *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			javaLangInstance = jl
			break
		}
	}
	if javaLangInstance == nil {
		t.Fatal("javaLang not found in langs")
	}

	helperLabel := label.New("", pkg, testHelperLibname(suiteName))
	javaLangInstance.classExportCache[helperLabel.String()] = classExportInfo{
		classes:  []types.ClassName{types.NewClassName(javaPackage, "TestHelper")},
		testonly: true,
	}
	ix.AddRule(c, suiteRule, file)
	ix.Finish()

	pci := NewResolver(javaLangInstance).buildPackageClassIndex(c, javaPackage, ix)
	providers := pci.test["TestHelper"]
	if len(providers) != 1 || providers[0] != helperLabel {
		t.Fatalf("TestHelper providers = %v, want [%s]", providers, helperLabel)
	}
}

// fakeClassCrossResolver is a stand-in for an external gazelle plugin (e.g. a
// proto/wire generator) that contributes class-level java resolutions via the
// resolve.CrossResolver interface.
type fakeClassCrossResolver struct {
	classToLabel map[string]label.Label
}

func (f *fakeClassCrossResolver) CrossResolve(c *config.Config, ix *resolve.RuleIndex, imp resolve.ImportSpec, lang string) []resolve.FindResult {
	if imp.Lang != languageName {
		return nil
	}
	if l, ok := f.classToLabel[imp.Imp]; ok {
		return []resolve.FindResult{{Label: l}}
	}
	return nil
}

// TestCrossResolverClassLevelSplitPackage covers a package that is split between an
// in-repo java_library and an external plugin: the importer uses one class from each,
// and must end up depending on both targets. The external class is only reachable
// because class import specs are never indexed, so the class-level lookup always
// falls through to the registered CrossResolver.
func TestCrossResolverClassLevelSplitPackage(t *testing.T) {
	c, langs, _ := testConfig(t)

	mrslv, exts := InitTestResolversAndExtensions(langs)

	wireLabel := label.New("wire", "", "foo_wire")
	exts = append(exts, &fakeClassCrossResolver{
		classToLabel: map[string]label.Label{
			"com.example.foo.WireMessage": wireLabel,
		},
	})

	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)

	javaPackage := types.NewPackageName("com.example.foo")

	// In-repo provider of the package, declaring only the hand-written class.
	fooContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "foo",
    _packages = ["com.example.foo"],
    visibility = ["//:__subpackages__"],
)
`
	fooFile, err := rule.LoadData(filepath.Join("foo", "BUILD.bazel"), "foo", []byte(fooContent))
	if err != nil {
		t.Fatal(err)
	}

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}

	fooRule := fooFile.Rules[0]
	setPackagesPrivateAttr(fooRule)
	fooLabel := label.New("", "foo", "foo")
	jLang.classExportCache[fooLabel.String()] = classExportInfo{
		classes:  []types.ClassName{types.NewClassName(javaPackage, "HandWritten")},
		testonly: false,
	}
	ix.AddRule(c, fooRule, fooFile)
	ix.Finish()

	// Importer that uses both the in-repo class and the externally-provided one.
	importerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "app",
    srcs = ["App.java"],
    visibility = ["//:__subpackages__"],
)
`
	importerFile, err := rule.LoadData("BUILD.bazel", "", []byte(importerContent))
	if err != nil {
		t.Fatal(err)
	}
	importerRule := importerFile.Rules[0]

	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{types.NewPackageName("com.example.app")}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn([]types.ClassName{
			types.NewClassName(javaPackage, "HandWritten"),
			types.NewClassName(javaPackage, "WireMessage"),
		}, types.ClassNameLess),
		ExportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ExportedClassNames:   sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
		AnnotationProcessors: sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
	}

	mrslv.Resolver(importerRule, "").Resolve(c, ix, rc, importerRule, resolveInput, label.New("", "", "app"))

	got := importerRule.AttrStrings("deps")
	sort.Strings(got)
	want := []string{"//foo", "@wire//:foo_wire"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestMavenClassLevelSplitPackage covers a package with one in-repo helper and
// all remaining classes in a compact whole-package Maven index. The importer
// uses one class from each owner and must not assign the Maven class to the
// workspace helper merely because that helper is the package's lone workspace
// provider.
func TestMavenClassLevelSplitPackage(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)

	javaPackage := types.NewPackageName("com.google.common.primitives")
	localContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "local_helper",
    _packages = ["com.google.common.primitives"],
    visibility = ["//:__subpackages__"],
)
`
	localFile, err := rule.LoadData(filepath.Join("local", "BUILD.bazel"), "local", []byte(localContent))
	if err != nil {
		t.Fatal(err)
	}

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}

	localRule := localFile.Rules[0]
	setPackagesPrivateAttr(localRule)
	localLabel := label.New("", "local", "local_helper")
	jLang.classExportCache[localLabel.String()] = classExportInfo{
		classes:  []types.ClassName{types.NewClassName(javaPackage, "LocalHelper")},
		testonly: false,
	}
	ix.AddRule(c, localRule, localFile)
	ix.Finish()

	importerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "app",
    srcs = ["App.java"],
    visibility = ["//:__subpackages__"],
)
`
	importerFile, err := rule.LoadData("BUILD.bazel", "", []byte(importerContent))
	if err != nil {
		t.Fatal(err)
	}
	importerRule := importerFile.Rules[0]
	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{types.NewPackageName("com.example.app")}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn([]types.ClassName{
			types.NewClassName(javaPackage, "Ints"),
			types.NewClassName(javaPackage, "LocalHelper"),
		}, types.ClassNameLess),
		ExportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ExportedClassNames:   sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
		AnnotationProcessors: sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
	}

	mrslv.Resolver(importerRule, "").Resolve(c, ix, rc, importerRule, resolveInput, label.New("", "", "app"))

	got := importerRule.AttrStrings("deps")
	sort.Strings(got)
	want := []string{"//local:local_helper", "@maven//:com_google_guava_guava"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestMavenClassInExternalNamespaceWithLocalHelper(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)

	javaPackage := types.NewPackageName("io.grpc")
	localContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "grpc",
    _packages = ["io.grpc"],
    visibility = ["//:__subpackages__"],
)
`
	localFile, err := rule.LoadData(filepath.Join("common-cloud", "spanner", "src", "main", "java", "io", "grpc", "BUILD.bazel"), filepath.Join("common-cloud", "spanner", "src", "main", "java", "io", "grpc"), []byte(localContent))
	if err != nil {
		t.Fatal(err)
	}

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}
	jLang.mavenResolver = &externalGrpcMavenResolver{}

	localRule := localFile.Rules[0]
	setPackagesPrivateAttr(localRule)
	localLabel := label.New("", filepath.Join("common-cloud", "spanner", "src", "main", "java", "io", "grpc"), "grpc")
	jLang.classExportCache[localLabel.String()] = classExportInfo{
		classes:  []types.ClassName{types.NewClassName(javaPackage, "Deadline")},
		testonly: false,
	}
	ix.AddRule(c, localRule, localFile)
	ix.Finish()

	importerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "app",
    srcs = ["App.java"],
    visibility = ["//:__subpackages__"],
)
`
	importerFile, err := rule.LoadData("BUILD.bazel", "", []byte(importerContent))
	if err != nil {
		t.Fatal(err)
	}
	importerRule := importerFile.Rules[0]
	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{types.NewPackageName("com.example.app")}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn([]types.ClassName{
			types.NewClassName(javaPackage, "Deadline"),
			types.NewClassName(javaPackage, "ServerInterceptor"),
		}, types.ClassNameLess),
		ExportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ExportedClassNames:   sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
		AnnotationProcessors: sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
	}

	mrslv.Resolver(importerRule, "").Resolve(c, ix, rc, importerRule, resolveInput, label.New("", "", "app"))

	got := importerRule.AttrStrings("deps")
	sort.Strings(got)
	want := []string{
		"//common-cloud/spanner/src/main/java/io/grpc",
		"@maven//:io_grpc_grpc_api",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestMavenExportedClassInExternalNamespaceWithLocalHelper(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)

	javaPackage := types.NewPackageName("io.grpc")
	localContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "grpc",
    _packages = ["io.grpc"],
    visibility = ["//:__subpackages__"],
)
`
	localFile, err := rule.LoadData(filepath.Join("common-cloud", "spanner", "src", "main", "java", "io", "grpc", "BUILD.bazel"), filepath.Join("common-cloud", "spanner", "src", "main", "java", "io", "grpc"), []byte(localContent))
	if err != nil {
		t.Fatal(err)
	}

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}
	jLang.mavenResolver = &externalGrpcMavenResolver{}

	localRule := localFile.Rules[0]
	setPackagesPrivateAttr(localRule)
	localLabel := label.New("", filepath.Join("common-cloud", "spanner", "src", "main", "java", "io", "grpc"), "grpc")
	jLang.classExportCache[localLabel.String()] = classExportInfo{
		classes:  []types.ClassName{types.NewClassName(javaPackage, "Deadline")},
		testonly: false,
	}
	ix.AddRule(c, localRule, localFile)
	ix.Finish()

	importerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "grpc_exports",
    srcs = ["GrpcExports.java"],
    visibility = ["//:__subpackages__"],
)
`
	importerFile, err := rule.LoadData("BUILD.bazel", "", []byte(importerContent))
	if err != nil {
		t.Fatal(err)
	}
	importerRule := importerFile.Rules[0]
	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{types.NewPackageName("com.example.app")}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ImportedClasses:      sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
		ExportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ExportedClassNames: sorted_set.NewSortedSetFn([]types.ClassName{
			types.NewClassName(javaPackage, "Deadline"),
			types.NewClassName(javaPackage, "ServerInterceptor"),
		}, types.ClassNameLess),
		AnnotationProcessors: sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
	}

	mrslv.Resolver(importerRule, "").Resolve(c, ix, rc, importerRule, resolveInput, label.New("", "", "grpc_exports"))

	got := importerRule.AttrStrings("exports")
	sort.Strings(got)
	want := []string{
		"//common-cloud/spanner/src/main/java/io/grpc",
		"@maven//:io_grpc_grpc_api",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("exports mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestWorkspaceClassInOwnedPackage(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)

	javaPackage := types.NewPackageName("com.squareup.common.uuid")
	providerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "uuid",
    _packages = ["com.squareup.common.uuid"],
)
`
	providerFile, err := rule.LoadData(filepath.Join("common", "BUILD.bazel"), "common", []byte(providerContent))
	if err != nil {
		t.Fatal(err)
	}
	providerRule := providerFile.Rules[0]
	setPackagesPrivateAttr(providerRule)

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}
	providerLabel := label.New("", "common", "uuid")
	jLang.classExportCache[providerLabel.String()] = classExportInfo{
		classes:  []types.ClassName{types.NewClassName(javaPackage, "UuidV7Supplier")},
		testonly: false,
	}
	ix.AddRule(c, providerRule, providerFile)
	ix.Finish()

	consumerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "uuid_testing",
    srcs = ["FakeUuidSupplier.java"],
)
`
	consumerFile, err := rule.LoadData("BUILD.bazel", "", []byte(consumerContent))
	if err != nil {
		t.Fatal(err)
	}
	consumerRule := consumerFile.Rules[0]
	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn([]types.ClassName{
			types.NewClassName(javaPackage, "UuidV7Supplier"),
		}, types.ClassNameLess),
		ExportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ExportedClassNames:   sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
		AnnotationProcessors: sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
	}

	mrslv.Resolver(consumerRule, "").Resolve(c, ix, rc, consumerRule, resolveInput, label.New("", "", "uuid_testing"))

	got := consumerRule.AttrStrings("deps")
	want := []string{"//common:uuid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestMavenClassInOwnedPackage(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	ix.Finish()
	rc := testRemoteCache(nil)

	javaPackage := types.NewPackageName("com.google.common.primitives")
	content := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "app",
    srcs = ["App.java"],
    visibility = ["//:__subpackages__"],
)
`
	f, err := rule.LoadData("BUILD.bazel", "", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	r := f.Rules[0]
	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn([]types.ClassName{
			types.NewClassName(javaPackage, "Ints"),
		}, types.ClassNameLess),
		ExportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess),
		ExportedClassNames:   sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
		AnnotationProcessors: sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess),
	}

	mrslv.Resolver(r, "").Resolve(c, ix, rc, r, resolveInput, label.New("", "", "app"))

	got := r.AttrStrings("deps")
	want := []string{"@maven//:com_google_guava_guava"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestPackageResolveDirectiveWinsOverImportedClassFallback(t *testing.T) {
	c, langs, configurers := testConfig(t)
	f, err := rule.LoadData("BUILD.bazel", "", []byte(`# gazelle:resolve java kotlin.test @maven//:org_jetbrains_kotlin_kotlin_test_junit5
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, configurer := range configurers {
		configurer.Configure(c, "", f)
	}
	c.Exts[languageName].(javaconfig.Configs)[""] = javaconfig.New(c.RepoRoot)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	ix.Finish()

	testRule := rule.NewRule("java_library", "test")
	testRule.SetAttr("srcs", []string{"Test.java"})
	emptyPackages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	emptyClasses := sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess)
	javaPackage := types.NewPackageName("kotlin.test")
	resolveInput := types.ResolveInput{
		PackageNames:         emptyPackages,
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn(
			[]types.ClassName{types.NewClassName(javaPackage, "Test")},
			types.ClassNameLess,
		),
		ExportedPackageNames: emptyPackages,
		ExportedClassNames:   emptyClasses,
		AnnotationProcessors: emptyClasses,
	}

	mrslv.Resolver(testRule, "").Resolve(c, ix, testRemoteCache(nil), testRule, resolveInput, label.New("", "", "test"))

	want := []string{"@maven//:org_jetbrains_kotlin_kotlin_test_junit5"}
	if got := testRule.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestResolvedWorkspacePackageKeepsUndeclaredClassFromCompactMavenFallback(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)

	javaPackage := types.NewPackageName("com.example.finance.common")
	providerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "common",
    _packages = ["com.example.finance.common"],
)
`
	providerFile, err := rule.LoadData(filepath.Join("common", "BUILD.bazel"), "common", []byte(providerContent))
	if err != nil {
		t.Fatal(err)
	}
	providerRule := providerFile.Rules[0]
	setPackagesPrivateAttr(providerRule)
	ix.AddRule(c, providerRule, providerFile)
	ix.Finish()

	consumer := rule.NewRule("kt_jvm_library", "consumer")
	consumer.SetAttr("srcs", []string{"Consumer.kt"})
	emptyPackages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	emptyClasses := sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess)
	resolveInput := types.ResolveInput{
		PackageNames:         emptyPackages,
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn(
			[]types.ClassName{types.NewClassName(javaPackage, "topLevelHelper")},
			types.ClassNameLess,
		),
		ExportedPackageNames: emptyPackages,
		ExportedClassNames:   emptyClasses,
		AnnotationProcessors: emptyClasses,
	}

	mrslv.Resolver(consumer, "").Resolve(c, ix, testRemoteCache(nil), consumer, resolveInput, label.New("", "", "consumer"))

	want := []string{"//common"}
	if got := consumer.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestPublishedWorkspacePackageDoesNotUseCompactMavenFallback(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)

	javaPackage := types.NewPackageName("com.example.finance.common.logging")
	providerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "logging",
    _packages = ["com.example.finance.common.logging"],
)
`
	providerFile, err := rule.LoadData(filepath.Join("common", "logging", "BUILD.bazel"), filepath.Join("common", "logging"), []byte(providerContent))
	if err != nil {
		t.Fatal(err)
	}
	providerRule := providerFile.Rules[0]
	setPackagesPrivateAttr(providerRule)

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}
	jLang.mavenResolver = &publishedWorkspaceMavenResolver{}
	providerLabel := label.New("", filepath.Join("common", "logging"), "logging")
	jLang.classExportCache[providerLabel.String()] = classExportInfo{
		classes:  []types.ClassName{types.NewClassName(javaPackage, "CommonClass")},
		testonly: false,
	}
	ix.AddRule(c, providerRule, providerFile)
	ix.Finish()

	consumer := rule.NewRule("kt_jvm_library", "consumer")
	consumer.SetAttr("srcs", []string{"Consumer.kt"})
	emptyPackages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	emptyClasses := sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess)
	resolveInput := types.ResolveInput{
		PackageNames:         emptyPackages,
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn(
			[]types.ClassName{
				types.NewClassName(javaPackage, "CommonClass"),
				types.NewClassName(javaPackage, "topLevelHelper"),
			},
			types.ClassNameLess,
		),
		ExportedPackageNames: emptyPackages,
		ExportedClassNames:   emptyClasses,
		AnnotationProcessors: emptyClasses,
	}

	mrslv.Resolver(consumer, "").Resolve(c, ix, testRemoteCache(nil), consumer, resolveInput, label.New("", "", "consumer"))

	want := []string{"//common/logging"}
	if got := consumer.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestResolvedWorkspacePackageKeepsUndeclaredClassFromPublishedMavenClassFallback(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)

	javaPackage := types.NewPackageName("com.example.util")
	providerContent := `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "util",
    _packages = ["com.example.util"],
)
`
	providerPkg := filepath.Join("common", "src", "main", "kotlin", "com", "example", "util")
	providerFile, err := rule.LoadData(filepath.Join(providerPkg, "BUILD.bazel"), providerPkg, []byte(providerContent))
	if err != nil {
		t.Fatal(err)
	}
	providerRule := providerFile.Rules[0]
	setPackagesPrivateAttr(providerRule)

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}
	jLang.mavenResolver = &publishedWorkspaceMavenResolver{}

	ix.AddRule(c, providerRule, providerFile)
	ix.Finish()

	from := label.New("", "service/src/main/kotlin/com/example/finance", "actions")
	c.Exts[languageName].(javaconfig.Configs)[from.Pkg] = javaconfig.New(c.RepoRoot)
	consumer := rule.NewRule("kt_jvm_library", "actions")
	consumer.SetAttr("srcs", []string{"Actions.kt"})
	emptyPackages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	emptyClasses := sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess)
	resolveInput := types.ResolveInput{
		PackageNames:         emptyPackages,
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn(
			[]types.ClassName{types.NewClassName(javaPackage, "KotlinDuration")},
			types.ClassNameLess,
		),
		ExportedPackageNames: emptyPackages,
		ExportedClassNames:   emptyClasses,
		AnnotationProcessors: emptyClasses,
	}

	mrslv.Resolver(consumer, "").Resolve(c, ix, testRemoteCache(nil), consumer, resolveInput, from)

	want := []string{"//common/src/main/kotlin/com/example/util"}
	if got := consumer.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestOwnedPackageDoesNotResolveToPublishedWorkspaceMavenArtifact(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	ix.Finish()

	var jLang *javaLang
	for _, lang := range langs {
		if jl, ok := lang.(*javaLang); ok {
			jLang = jl
			break
		}
	}
	if jLang == nil {
		t.Fatal("javaLang not found in langs")
	}
	jLang.mavenResolver = &publishedWorkspaceMavenResolver{}

	from := label.New("", "service/src/main/kotlin/com/example/finance", "actions")
	c.Exts[languageName].(javaconfig.Configs)[from.Pkg] = javaconfig.New(c.RepoRoot)

	javaPackage := types.NewPackageName("com.example.finance")
	consumer := rule.NewRule("kt_jvm_library", "actions")
	consumer.SetAttr("srcs", []string{"Actions.kt"})
	emptyPackages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	emptyClasses := sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess)
	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses:      emptyClasses,
		ExportedPackageNames: emptyPackages,
		ExportedClassNames:   emptyClasses,
		AnnotationProcessors: emptyClasses,
	}

	mrslv.Resolver(consumer, "").Resolve(c, ix, testRemoteCache(nil), consumer, resolveInput, from)

	if got := consumer.AttrStrings("deps"); len(got) != 0 {
		t.Errorf("deps mismatch:\n got: %v\nwant: []", got)
	}
}

func TestMavenLabelLooksLikeWorkspaceOwner(t *testing.T) {
	tests := []struct {
		name       string
		mavenLabel label.Label
		owner      label.Label
		want       bool
	}{
		{
			name:       "published subpackage owner",
			mavenLabel: label.New("maven", "", "com_example_finance_common"),
			owner:      label.New("", "common/src/main/kotlin/com/example/finance/common/logging", "logging"),
			want:       true,
		},
		{
			name:       "published root package owner",
			mavenLabel: label.New("maven", "", "com_example_finance_common"),
			owner:      label.New("", "service/src/main/kotlin/com/example/finance", "actions"),
			want:       true,
		},
		{
			name:       "external split package helper",
			mavenLabel: label.New("maven", "", "com_google_guava_guava"),
			owner:      label.New("", "local", "local_helper"),
			want:       false,
		},
		{
			name:       "shared organisation tokens without owner leaf",
			mavenLabel: label.New("maven", "", "com_example_app_common"),
			owner:      label.New("", "service/src/main/kotlin/com/example/billing", "actions"),
			want:       false,
		},
		{
			name:       "two shared owner tokens without leaf match",
			mavenLabel: label.New("maven", "", "com_example_billing_payments"),
			owner:      label.New("", "service/src/main/kotlin/com/example/billing/actions", "actions"),
			want:       false,
		},
		{
			name:       "two shared owner tokens with leaf match",
			mavenLabel: label.New("maven", "", "com_example_billing_payments"),
			owner:      label.New("", "service/src/main/kotlin/com/example/actions/billing", "actions"),
			want:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mavenLabelLooksLikeWorkspaceOwner(tc.mavenLabel, tc.owner); got != tc.want {
				t.Fatalf("mavenLabelLooksLikeWorkspaceOwner() = %t, want %t", got, tc.want)
			}
		})
	}
}

type publishedWorkspaceMavenResolver struct{}

func (*publishedWorkspaceMavenResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	if pkg.Name == "com.example.finance" || pkg.Name == "com.example.finance.common" || pkg.Name == "com.example.finance.common.logging" {
		return label.New(mavenRepositoryName, "", "com_example_finance_common"), nil
	}
	return label.NoLabel, &maven.NoExternalImportsError{PackageName: pkg.Name}
}

func (*publishedWorkspaceMavenResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	if className.FullyQualifiedClassName() == "com.example.util.KotlinDuration" {
		return label.New(mavenRepositoryName, "", "com_example_finance_common"), nil
	}
	return label.NoLabel, nil
}

func TestKotlinTestAssociatesOnlyKotlinProductionLibrary(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mainKind       string
		mainSource     string
		wantAssociates []string
		wantDeps       []string
	}{
		{
			name:       "java production library stays a dependency",
			mainKind:   "java_library",
			mainSource: "Main.java",
			wantDeps:   []string{"//:main", ":unrelated"},
		},
		{
			name:           "kotlin production library becomes an associate",
			mainKind:       "kt_jvm_library",
			mainSource:     "Main.kt",
			wantAssociates: []string{":main"},
			wantDeps:       []string{":unrelated"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, langs, _ := testConfig(t)
			mrslv, exts := InitTestResolversAndExtensions(langs)
			ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)

			content := fmt.Sprintf(`%s(
    name = "main",
    srcs = ["%s"],
    _packages = ["com.example"],
)`, tc.mainKind, tc.mainSource)
			f, err := rule.LoadData("BUILD.bazel", "", []byte(content))
			if err != nil {
				t.Fatal(err)
			}
			mainRule := f.Rules[0]
			setPackagesPrivateAttr(mainRule)
			ix.AddRule(c, mainRule, f)

			jLang := langs[1].(*javaLang)
			if tc.mainKind == "kt_jvm_library" {
				jLang.kotlinLibraries[label.New("", "", "main").String()] = true
			}
			ix.Finish()

			testRule := rule.NewRule("kt_jvm_test", "test")
			testRule.SetAttr("srcs", []string{"Test.kt"})
			oldDepsExpr := &bzl.ListExpr{List: []bzl.Expr{
				&bzl.StringExpr{Value: "//:main"},
				&bzl.StringExpr{Value: ":unrelated"},
			}}
			testRule.SetAttr("deps", oldDepsExpr)
			resolveInput := types.ResolveInput{
				PackageNames: sorted_set.NewSortedSetFn(
					[]types.PackageName{types.NewPackageName("com.example")},
					types.PackageNameLess,
				),
			}

			jLang.Resolver.(*Resolver).populateAssociatesAttr(
				c, ix, resolveInput, testRule, true, label.New("", "", "test"),
			)

			if tc.mainKind == "kt_jvm_library" && testRule.Attr("deps") == oldDepsExpr {
				t.Fatal("deps still references the destination AST expression")
			}
			if got := testRule.AttrStrings("associates"); !reflect.DeepEqual(got, tc.wantAssociates) {
				t.Errorf("associates mismatch:\n got: %v\nwant: %v", got, tc.wantAssociates)
			}
			if got := testRule.AttrStrings("deps"); !reflect.DeepEqual(got, tc.wantDeps) {
				t.Errorf("deps mismatch:\n got: %v\nwant: %v", got, tc.wantDeps)
			}
		})
	}
}

// A package split across several production rules never yields an associate:
// associate compilation joins the test into the production Kotlin module, which
// changes language semantics (internal visibility, smart-cast stability), so it
// is only safe when the package maps to exactly one library. The class owner
// stays an ordinary dependency instead.
func TestKotlinTestAssociatesSkipSplitPackageProduction(t *testing.T) {
	c, langs, _ := testConfig(t)
	c.RepoName = "java"
	from := label.New("", "service/billing/src/test/kotlin/com/example/billing", "BalanceFacadeTest")
	c.Exts[languageName].(javaconfig.Configs)[from.Pkg] = javaconfig.New(c.RepoRoot)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	jLang := langs[1].(*javaLang)

	const mainPkg = "service/billing/src/main/kotlin/com/example/billing"
	javaPackage := types.NewPackageName("com.example.billing")
	f, err := rule.LoadData(filepath.Join(mainPkg, "BUILD.bazel"), mainPkg, []byte(`kt_jvm_library(
    name = "balances",
    srcs = ["BalanceSnapshot.kt"],
    _packages = ["com.example.billing"],
)

kt_jvm_library(
    name = "config",
    srcs = ["BillingConfig.kt"],
    _packages = ["com.example.billing"],
)
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, productionRule := range f.Rules {
		setPackagesPrivateAttr(productionRule)
		productionLabel := label.New("", f.Pkg, productionRule.Name())
		jLang.kotlinLibraries[productionLabel.String()] = true
		className := strings.TrimSuffix(productionRule.AttrStrings("srcs")[0], ".kt")
		jLang.classExportCache[productionLabel.String()] = classExportInfo{
			classes: []types.ClassName{types.NewClassName(javaPackage, className)},
		}
		ix.AddRule(c, productionRule, f)
	}
	ix.Finish()

	mainSpec := resolve.ImportSpec{Lang: languageName, Imp: types.NewResolvableJavaPackage(javaPackage, false, false).String()}
	if got := len(ix.FindRulesByImportWithConfig(c, mainSpec, languageName)); got != 2 {
		t.Fatalf("indexed production providers = %d, want 2", got)
	}

	testRule := rule.NewRule("java_test_suite", "BalanceFacadeTest")
	testRule.SetAttr("srcs", []string{"BalanceFacadeTest.kt"})
	emptyPackages := sorted_set.NewSortedSetFn([]types.PackageName{}, types.PackageNameLess)
	emptyClasses := sorted_set.NewSortedSetFn([]types.ClassName{}, types.ClassNameLess)
	resolveInput := types.ResolveInput{
		PackageNames:         sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedPackageNames: sorted_set.NewSortedSetFn([]types.PackageName{javaPackage}, types.PackageNameLess),
		ImportedClasses: sorted_set.NewSortedSetFn(
			[]types.ClassName{types.NewClassName(javaPackage, "BalanceSnapshot")},
			types.ClassNameLess,
		),
		ExportedPackageNames: emptyPackages,
		ExportedClassNames:   emptyClasses,
		AnnotationProcessors: emptyClasses,
	}
	resolver := jLang.Resolver.(*Resolver)
	associateLabels := resolver.productionAssociateLabels(c, ix, javaPackage, from)
	if got := associateLabels.SortedSlice(); len(got) != 0 {
		t.Fatalf("productionAssociateLabels() = %v, want none for a split package", got)
	}
	resolver.Resolve(c, ix, testRemoteCache(nil), testRule, resolveInput, from)

	if got := testRule.AttrStrings("associates"); len(got) != 0 {
		t.Fatalf("associates = %v, want none", got)
	}
	want := []string{"//service/billing/src/main/kotlin/com/example/billing:balances"}
	if got := testRule.AttrStrings("deps"); !reflect.DeepEqual(got, want) {
		t.Fatalf("deps = %v, want %v", got, want)
	}
}

func TestJavaTestSuiteManagesResolvedPackageAssociate(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	jLang := langs[1].(*javaLang)
	kinds := jLang.Kinds()

	if !kinds["java_test_suite"].ResolveAttrs["associates"] {
		t.Fatal("java_test_suite does not manage associates during the post-resolve merge")
	}
	for _, kind := range []string{"java_binary", "java_test"} {
		if kinds[kind].ResolveAttrs["associates"] {
			t.Errorf("%s unexpectedly manages associates", kind)
		}
	}

	const productionPkg = "src/main/kotlin/com/example"
	javaPackage := types.NewPackageName("com.example")
	productionFile, err := rule.LoadData(
		filepath.Join(productionPkg, "BUILD.bazel"),
		productionPkg,
		[]byte(`kt_jvm_library(
    name = "example",
    srcs = ["Main.kt"],
    _packages = ["com.example"],
)`),
	)
	if err != nil {
		t.Fatal(err)
	}
	productionRule := productionFile.Rules[0]
	setPackagesPrivateAttr(productionRule)
	productionLabel := label.New("", productionPkg, "example")
	jLang.kotlinLibraries[productionLabel.String()] = true
	ix.AddRule(c, productionRule, productionFile)
	ix.Finish()

	generated := rule.NewRule("java_test_suite", "example")
	generated.SetAttr("srcs", []string{"ExampleTest.kt"})
	generated.SetAttr("deps", []string{productionLabel.String(), ":unrelated"})
	resolveInput := types.ResolveInput{
		PackageNames: sorted_set.NewSortedSetFn(
			[]types.PackageName{javaPackage},
			types.PackageNameLess,
		),
	}
	from := label.New("", "src/test/kotlin/com/example", "example")
	jLang.Resolver.(*Resolver).populateAssociatesAttr(c, ix, resolveInput, generated, true, from)

	wantAssociate := simplifyLabel(c.RepoName, productionLabel, from).String()
	if got := generated.AttrStrings("associates"); !reflect.DeepEqual(got, []string{wantAssociate}) {
		t.Fatalf("generated associates mismatch:\n got: %v\nwant: %v", got, []string{wantAssociate})
	}
	if got := generated.AttrStrings("deps"); !reflect.DeepEqual(got, []string{":unrelated"}) {
		t.Fatalf("generated deps mismatch:\n got: %v\nwant: %v", got, []string{":unrelated"})
	}

	existingFile, err := rule.LoadData("BUILD.bazel", from.Pkg, []byte(`java_test_suite(
    name = "example",
    srcs = ["ExampleTest.kt"],
    associates = ["//stale:main"],
    deps = ["//stale:dep"],
)`))
	if err != nil {
		t.Fatal(err)
	}
	existing := existingFile.Rules[0]
	rule.MergeRules(generated, existing, kinds["java_test_suite"].ResolveAttrs, existingFile.Path)

	if got := existing.AttrStrings("associates"); !reflect.DeepEqual(got, []string{wantAssociate}) {
		t.Errorf("merged associates mismatch:\n got: %v\nwant: %v", got, []string{wantAssociate})
	}
	if got := existing.AttrStrings("deps"); !reflect.DeepEqual(got, []string{":unrelated"}) {
		t.Errorf("merged deps mismatch:\n got: %v\nwant: %v", got, []string{":unrelated"})
	}
}

// TestRuleIsTestOnly covers `testonly = True` detection across the two shapes
// Gazelle has used to store the attribute: older Gazelle emitted
// `*bzl.LiteralExpr{Token: "True"}` for `SetAttr("testonly", true)`; current
// Gazelle emits `*bzl.Ident{Name: "True"}`. Both must be recognised so
// `isTestRule` stays consistent when a BUILD file is round-tripped through
// Gazelle across versions.
func TestRuleIsTestOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		attr bzl.Expr
		want bool
	}{
		"unset":                    {attr: nil, want: false},
		"ident-true":               {attr: &bzl.Ident{Name: "True"}, want: true},
		"ident-false":              {attr: &bzl.Ident{Name: "False"}, want: false},
		"literalexpr-true":         {attr: &bzl.LiteralExpr{Token: "True"}, want: true},
		"literalexpr-false":        {attr: &bzl.LiteralExpr{Token: "False"}, want: false},
		"string-true-not-testonly": {attr: &bzl.StringExpr{Value: "True"}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			r := rule.NewRule("java_library", "example")
			if tc.attr != nil {
				r.SetAttr("testonly", tc.attr)
			}
			if got := ruleIsTestOnly(r); got != tc.want {
				t.Errorf("ruleIsTestOnly(%T = %v) = %v, want %v", tc.attr, tc.attr, got, tc.want)
			}
		})
	}
}

// TestRuleIsTestOnlyRoundTrip pins down what `Rule.SetAttr("testonly", true)`
// stores in the current Gazelle release. If Gazelle ever changes the encoding
// again (e.g. back to LiteralExpr, or to a new shape), this test tells us to
// extend `ruleIsTestOnly` to cover it.
func TestRuleIsTestOnlyRoundTrip(t *testing.T) {
	r := rule.NewRule("java_library", "example")
	r.SetAttr("testonly", true)
	if !ruleIsTestOnly(r) {
		t.Errorf("ruleIsTestOnly must recognise the shape emitted by SetAttr; attr = %#v", r.Attr("testonly"))
	}
}

// TestResolveTestOnlyIdentSuppressesOwnPackageError is the end-to-end guard.
// A testonly library that imports its own package (no external provider) must
// be treated as a test rule so `isTestRule && ownPackageNames.Contains(imp)`
// silently skips the import instead of raising an "Unable to find package"
// error and setting hasHadErrors. The BUILD input uses `testonly = True`
// encoded as *bzl.Ident, matching what current Gazelle emits after a round
// trip through the parser.
func TestResolveTestOnlyIdentSuppressesOwnPackageError(t *testing.T) {
	c, langs, _ := testConfig(t)
	mrslv, exts := InitTestResolversAndExtensions(langs)
	ix := resolve.NewRuleIndex(mrslv.Resolver, exts...)
	rc := testRemoteCache(nil)

	// Locate the javaLang so we can inspect hasHadErrors after Resolve.
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
	// Report unresolved packages as NoExternalImportsError (the shape a real
	// Maven resolver returns) so Resolve falls through to the isTestRule check
	// instead of calling logger.Fatal on the "unexpected import" error the
	// default TestMavenResolver raises.
	jLang.mavenResolver = &noExternalMavenResolver{}

	const content = `load("@rules_java//java:defs.bzl", "java_library")

java_library(
    name = "helpers",
    srcs = ["Helpers.java"],
    testonly = True,
    _imported_packages = ["com.example.helpers"],
    _packages = ["com.example.helpers"],
    visibility = ["//:__subpackages__"],
)`
	f, err := rule.LoadData("BUILD.bazel", "", []byte(content))
	if err != nil {
		t.Fatal(err)
	}

	// Pre-condition: the parser lands `testonly = True` as *bzl.Ident. If Gazelle
	// ever changes this again we want the test to say so, not silently pass because
	// the LiteralExpr branch still fires.
	got := f.Rules[0].Attr("testonly")
	if _, ok := got.(*bzl.Ident); !ok {
		t.Fatalf("test precondition violated: parsed `testonly = True` as %T, expected *bzl.Ident", got)
	}

	imports := make([]interface{}, len(f.Rules))
	for i, r := range f.Rules {
		imports[i] = convertImportsAttr(r)
		ix.AddRule(c, r, f)
	}
	ix.Finish()
	for i, r := range f.Rules {
		mrslv.Resolver(r, "").Resolve(c, ix, rc, r, imports[i], label.New("", "", r.Name()))
	}

	if jLang.hasHadErrors {
		t.Errorf("Resolve set hasHadErrors on a testonly library importing its own package; the isTestRule check likely missed `testonly = True` stored as *bzl.Ident")
	}
}

// noExternalMavenResolver reports every package as unresolved via a
// *maven.NoExternalImportsError, mirroring what the real resolver returns for
// packages that no artifact provides.
type noExternalMavenResolver struct{}

func (r *noExternalMavenResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, &maven.NoExternalImportsError{PackageName: pkg.Name}
}

func (r *noExternalMavenResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, nil
}
