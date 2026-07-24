package gazelle

import (
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/maven"
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/label"
)

type prefixMavenResolver map[string]label.Label

func (r prefixMavenResolver) Resolve(pkg types.PackageName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	if l, ok := r[pkg.Name]; ok {
		return l, nil
	}
	return label.NoLabel, &maven.NoExternalImportsError{PackageName: pkg.Name}
}

func (r prefixMavenResolver) ResolveClass(className types.ClassName, excludedArtifacts map[string]struct{}, mavenRepositoryName string) (label.Label, error) {
	return label.NoLabel, nil
}

func TestResolveMavenWholePackageClassLongestPrefix(t *testing.T) {
	className, err := types.ParseClassName("org.xbill.DNS.Lookup")
	if err != nil {
		t.Fatal(err)
	}
	want := label.New("maven", "", "dnsjava")
	lang := &javaLang{mavenResolver: prefixMavenResolver{
		"org":           label.New("maven", "", "too_short"),
		"org.xbill.DNS": want,
	}}
	resolver := NewResolver(lang)
	config := javaconfig.New(".")
	got, err := resolver.resolveMavenWholePackageClass(config, *className)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolveMavenWholePackageClass() = %s, want %s", got, want)
	}
}

func TestResolveMavenWholePackageClassNoUniquePrefix(t *testing.T) {
	className, err := types.ParseClassName("org.apache.spark.sql.functions.col")
	if err != nil {
		t.Fatal(err)
	}
	lang := &javaLang{mavenResolver: prefixMavenResolver{}}
	resolver := NewResolver(lang)
	got, err := resolver.resolveMavenWholePackageClass(javaconfig.New("."), *className)
	if err != nil {
		t.Fatal(err)
	}
	if got != label.NoLabel {
		t.Fatalf("resolveMavenWholePackageClass() = %s, want no label", got)
	}
}
