package gazelle

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/javaparser"
	"github.com/bazelbuild/bazel-gazelle/config"
)

func TestJavaBatchRootsFollowGazelleArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "child"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		workDir string
		args    []string
		want    []javaparser.SourceRoot
	}{
		{"repository", root, nil, []javaparser.SourceRoot{{Rel: "", Recursive: true}}},
		{"subdirectory", root, []string{"src"}, []javaparser.SourceRoot{{Rel: "src", Recursive: true}}},
		{"working directory", filepath.Join(root, "src"), nil, []javaparser.SourceRoot{{Rel: "src", Recursive: true}}},
		{"non recursive", root, []string{"-r=false", "src"}, []javaparser.SourceRoot{{Rel: "src", Recursive: false}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fs := flag.NewFlagSet(test.name, flag.ContinueOnError)
			fs.Bool("r", true, "")
			if err := fs.Parse(test.args); err != nil {
				t.Fatal(err)
			}
			got, err := javaSourceRootsFromFlags(fs, &config.Config{RepoRoot: root, WorkDir: test.workDir})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("roots = %v, want %v", got, test.want)
			}
		})
	}
}

func TestJavaBatchRootsRejectPathsOutsideRepository(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	fs := flag.NewFlagSet("outside", flag.ContinueOnError)
	fs.Bool("r", true, "")
	if err := fs.Parse([]string{outside}); err != nil {
		t.Fatal(err)
	}
	if _, err := javaSourceRootsFromFlags(fs, &config.Config{RepoRoot: root, WorkDir: root}); err == nil {
		t.Fatal("path outside the repository was accepted")
	}
}

func TestJavaBatchSizeMustNotBeNegative(t *testing.T) {
	c := &config.Config{Exts: make(map[string]interface{})}
	configurer := NewConfigurer(NewLanguage().(*javaLang))
	fs := flag.NewFlagSet("negative batch size", flag.ContinueOnError)
	configurer.RegisterFlags(fs, "update", c)
	if err := fs.Parse([]string{"-java-batch-size=-1"}); err != nil {
		t.Fatal(err)
	}
	if err := configurer.CheckFlags(fs, c); err == nil {
		t.Fatal("negative batch size was accepted")
	}
}

func TestJavaParserWorkerFlags(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		want      int
		wantError bool
	}{
		{"automatic", nil, 0, false},
		{"override", []string{"-java-parser-workers=4"}, 4, false},
		{"negative", []string{"-java-parser-workers=-1"}, -1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := &config.Config{Exts: make(map[string]interface{})}
			configurer := NewConfigurer(NewLanguage().(*javaLang))
			fs := flag.NewFlagSet(test.name, flag.ContinueOnError)
			configurer.RegisterFlags(fs, "update", c)
			if err := fs.Parse(test.args); err != nil {
				t.Fatal(err)
			}
			err := configurer.CheckFlags(fs, c)
			if (err != nil) != test.wantError || configurer.javaParserWorkers != test.want {
				t.Fatalf("workers = %d, error = %v", configurer.javaParserWorkers, err)
			}
		})
	}
}
