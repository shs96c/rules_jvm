// Package otherprovidertest is a Gazelle language extension used only by the
// contrib_rules_jvm Java plugin's integration tests. It emits a single
// fake_java_provider rule for any directory containing a "provided_java.txt"
// file, with the standardized OtherGen private-attribute contract attached
// (java_provided_classes / java_provided_packages). The Java extension scrapes
// those attributes during its own GenerateRules pass to route Java imports to
// the producing target — this plugin lets us exercise that path without
// pulling in a real upstream producer.
//
// The marker file format is one fully-qualified class name per line, with
// blank lines and lines starting with "#" ignored. Java packages are derived
// from the FQNs by stripping the trailing class component.
package otherprovidertest

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

const (
	languageName = "fake_java_provider"
	markerFile   = "provided_java.txt"
	ruleKind     = "fake_java_provider"
	ruleName     = "fake_provider"

	providedClassesKey  = "java_provided_classes"
	providedPackagesKey = "java_provided_packages"
)

// NewLanguage is the entry point Gazelle uses to instantiate the plugin.
func NewLanguage() language.Language {
	return &fakeLang{}
}

type fakeLang struct{}

func (*fakeLang) Name() string { return languageName }

func (*fakeLang) RegisterFlags(_ *flag.FlagSet, _ string, _ *config.Config) {}
func (*fakeLang) CheckFlags(_ *flag.FlagSet, _ *config.Config) error        { return nil }
func (*fakeLang) KnownDirectives() []string                                 { return nil }
func (*fakeLang) Configure(_ *config.Config, _ string, _ *rule.File)        {}

func (*fakeLang) Kinds() map[string]rule.KindInfo {
	return map[string]rule.KindInfo{
		ruleKind: {
			NonEmptyAttrs:  map[string]bool{"name": true},
			MergeableAttrs: map[string]bool{},
		},
	}
}

func (*fakeLang) Loads() []rule.LoadInfo {
	return []rule.LoadInfo{{
		Name:    "@fake_java_provider//:rules.bzl",
		Symbols: []string{ruleKind},
	}}
}

func (*fakeLang) GenerateRules(args language.GenerateArgs) language.GenerateResult {
	if !hasMarker(args.RegularFiles) {
		return language.GenerateResult{}
	}

	classes, err := readClasses(filepath.Join(args.Dir, markerFile))
	if err != nil || len(classes) == 0 {
		return language.GenerateResult{}
	}
	packages := derivePackages(classes)

	r := rule.NewRule(ruleKind, ruleName)
	r.SetPrivateAttr(providedClassesKey, classes)
	r.SetPrivateAttr(providedPackagesKey, packages)

	return language.GenerateResult{
		Gen:     []*rule.Rule{r},
		Imports: []interface{}{nil},
	}
}

func (*fakeLang) Fix(_ *config.Config, _ *rule.File) {}

func (*fakeLang) Imports(_ *config.Config, _ *rule.Rule, _ *rule.File) []resolve.ImportSpec {
	return nil
}

func (*fakeLang) Embeds(_ *rule.Rule, _ label.Label) []label.Label { return nil }

func (*fakeLang) Resolve(_ *config.Config, _ *resolve.RuleIndex, _ *repo.RemoteCache, _ *rule.Rule, _ interface{}, _ label.Label) {
}

func hasMarker(files []string) bool {
	for _, f := range files {
		if f == markerFile {
			return true
		}
	}
	return false
}

func readClasses(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func derivePackages(classes []string) []string {
	seen := map[string]struct{}{}
	var pkgs []string
	for _, fqn := range classes {
		idx := strings.LastIndex(fqn, ".")
		if idx <= 0 {
			continue
		}
		pkg := fqn[:idx]
		if _, ok := seen[pkg]; ok {
			continue
		}
		seen[pkg] = struct{}{}
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	return pkgs
}
