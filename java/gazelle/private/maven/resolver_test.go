package maven

import (
	"os"
	"reflect"
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/rs/zerolog"
)

func TestResolver(t *testing.T) {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).
		With().
		Caller().
		Logger().
		Level(zerolog.DebugLevel)

	m := make(map[string]struct{})
	m["@maven//:com_google_j2objc_j2objc_annotations"] = struct{}{}
	r, err := NewResolver(
		WithInstallFile("testdata/guava_maven_install.json"),
		WithLogger(logger),
	)
	if err != nil {
		t.Fatal(err)
	}

	assertResolves(t, r, m, "com.google.common.collect", "@maven//:com_google_guava_guava")
	assertResolves(t, r, m, "javax.annotation", "@maven//:com_google_code_findbugs_jsr305")
	got, err := r.Resolve(types.NewPackageName("unknown.package"), m, "maven")
	if err == nil {
		t.Errorf("Want error finding label for unknown.package, got %v", got)
	}
	got, err = r.Resolve(types.NewPackageName("com.google.j2objc.annotations"), m, "maven")
	if err == nil {
		t.Errorf("Want error finding label for excluded artifact, got %v", got)
	}

}

func assertResolves(t *testing.T, r Resolver, excludePackages map[string]struct{}, pkg, wantLabelStr string) {
	got, err := r.Resolve(types.NewPackageName(pkg), excludePackages, "maven")
	if err != nil {
		t.Errorf("Error finding label for %v: %v", pkg, err)
	}
	want, _ := label.Parse(wantLabelStr)
	if got != want {
		t.Errorf("Incorrect label for %v; want %v got %v", pkg, want, got)
	}
}

// TestResolverClassifier checks that classes and packages which only exist in a
// Maven classifier jar (e.g. test-fixtures) resolve to a classifier-suffixed
// label, exercising both index sections.
func TestResolverClassifier(t *testing.T) {
	r, err := NewResolver(
		WithInstallFile("testdata/classifier_maven_install.json"),
		WithIndexFile("testdata/classifier_maven_index.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	none := make(map[string]struct{})

	// A package present only in a classifier jar (non-split, "packages" section).
	assertResolves(t, r, none, "net.sf.json.jdk15", "@maven//:net_sf_json_lib_json_lib_jdk15")
	// The plain jar still resolves to its plain label.
	assertResolves(t, r, none, "net.sf.json", "@maven//:net_sf_json_lib_json_lib")

	// A class present only in a test-fixtures jar, in a package shared with the
	// main jar (split package, "split_package_classes" section), resolves to the
	// test-fixtures label; the main jar's class resolves to the plain label.
	assertResolvesClass(t, r, none, "com.example.fixtures.WidgetFixtures", "@maven//:com_example_lib_test_fixtures")
	assertResolvesClass(t, r, none, "com.example.fixtures.Widget", "@maven//:com_example_lib")
	// Tiebreak: a class listed in BOTH the plain and the test-fixtures jar
	// resolves to the plain label
	assertResolvesClass(t, r, none, "com.example.fixtures.SharedHelper", "@maven//:com_example_lib")
	// rules_jvm_external omits nested classes from its index. Resolve a nested
	// source-level name through its indexed outer class instead.
	assertResolvesClass(t, r, none, "com.example.fixtures.Widget.Nested",
		"@maven//:com_example_lib")
}

func assertResolvesClass(t *testing.T, r Resolver, excludeArtifacts map[string]struct{}, className, wantLabelStr string) {
	t.Helper()
	cn, err := types.ParseClassName(className)
	if err != nil {
		t.Fatalf("parsing class name %q: %v", className, err)
	}
	got, err := r.ResolveClass(*cn, excludeArtifacts, "maven")
	if err != nil {
		t.Errorf("Error finding label for class %v: %v", className, err)
	}
	want, _ := label.Parse(wantLabelStr)
	if got != want {
		t.Errorf("Incorrect label for class %v; want %v got %v", className, want, got)
	}
}

func TestResolverMockKCompileCompanions(t *testing.T) {
	r, err := NewResolver(
		WithInstallFile("testdata/mockk_maven_install.json"),
		WithIndexFile("testdata/mockk_maven_index.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	compileResolver, ok := r.(CompileResolver)
	if !ok {
		t.Fatal("Maven resolver does not implement CompileResolver")
	}

	none := make(map[string]struct{})
	assertResolvesClass(t, r, none, "io.mockk.every", "@maven//:io_mockk_mockk_jvm")
	assertResolvesClass(t, r, none, "io.mockk.MockKAnnotations", "@maven//:io_mockk_mockk_jvm")
	assertResolvesClass(t, r, none, "io.mockk.Runs", "@maven//:io_mockk_mockk_dsl_jvm")
	assertResolvesClass(t, r, none, "io.mockk.MockKStubScope", "@maven//:io_mockk_mockk_dsl_jvm")
	assertResolvesClass(t, r, none, "io.mockk.junit5.MockKExtension", "@maven//:io_mockk_mockk_jvm")

	mockk := types.NewPackageName("io.mockk")
	t.Run("package-only import has a unique dependency root", func(t *testing.T) {
		got, err := compileResolver.ResolveCompilePackage(mockk, none, "maven")
		if err != nil {
			t.Fatal(err)
		}
		assertLabelStrings(t, got, []string{
			"@maven//:io_mockk_mockk_jvm",
			"@maven//:io_mockk_mockk_dsl_jvm",
		})
	})

	t.Run("jvm symbol gets its dsl compile companion", func(t *testing.T) {
		got := compileResolver.CompileCompanions(mockk, []label.Label{
			mustParseLabel(t, "@maven//:io_mockk_mockk_jvm"),
		}, none, "maven")
		assertLabelStrings(t, got, []string{
			"@maven//:io_mockk_mockk_jvm",
			"@maven//:io_mockk_mockk_dsl_jvm",
		})
	})

	t.Run("dsl symbol does not pull its dependent backwards", func(t *testing.T) {
		got := compileResolver.CompileCompanions(mockk, []label.Label{
			mustParseLabel(t, "@maven//:io_mockk_mockk_dsl_jvm"),
		}, none, "maven")
		assertLabelStrings(t, got, []string{"@maven//:io_mockk_mockk_dsl_jvm"})
	})

	t.Run("unique MockK subpackage stays on jvm", func(t *testing.T) {
		got, err := compileResolver.ResolveCompilePackage(types.NewPackageName("io.mockk.junit5"), none, "maven")
		if err != nil {
			t.Fatal(err)
		}
		assertLabelStrings(t, got, []string{"@maven//:io_mockk_mockk_jvm"})
	})

	t.Run("excluded companion stays excluded", func(t *testing.T) {
		excluded := map[string]struct{}{
			"@maven//:io_mockk_mockk_dsl_jvm": {},
		}
		got := compileResolver.CompileCompanions(mockk, []label.Label{
			mustParseLabel(t, "@maven//:io_mockk_mockk_jvm"),
		}, excluded, "maven")
		assertLabelStrings(t, got, []string{"@maven//:io_mockk_mockk_jvm"})
	})

	t.Run("unrelated split owners remain ambiguous", func(t *testing.T) {
		got, err := compileResolver.ResolveCompilePackage(types.NewPackageName("com.example.split"), none, "maven")
		if got != nil {
			t.Fatalf("ResolveCompilePackage() = %v, want no labels", got)
		}
		if _, ok := err.(*MultipleExternalImportsError); !ok {
			t.Fatalf("ResolveCompilePackage() error = %T %v, want *MultipleExternalImportsError", err, err)
		}
	})
}

func mustParseLabel(t *testing.T, value string) label.Label {
	t.Helper()
	l, err := label.Parse(value)
	if err != nil {
		t.Fatalf("parsing label %q: %v", value, err)
	}
	return l
}

func assertLabelStrings(t *testing.T, got []label.Label, want []string) {
	t.Helper()
	gotStrings := make([]string, 0, len(got))
	for _, l := range got {
		gotStrings = append(gotStrings, l.String())
	}
	if !reflect.DeepEqual(gotStrings, want) {
		t.Errorf("labels mismatch:\n got: %v\nwant: %v", gotStrings, want)
	}
}
