package gazelle

import (
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/javaconfig"
	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/bazel-gazelle/testtools"
	"github.com/stretchr/testify/require"
)

func TestFlagParsing(t *testing.T) {
	configurer := NewConfigurer(NewLanguage().(*javaLang))

	gazelleConfig := testtools.NewTestConfig(t,
		[]config.Configurer{configurer},
		[]language.Language{},
		[]string{
			"-java-annotation-to-attribute=com.example.annotations.FlakyTest=flaky=True",
			"-java-maven-install-file=install_maven.json",
		})

	// Command line value made it to the configurer
	require.Equal(t, "install_maven.json", configurer.mavenInstallFile)

	// Command line value made it to the java config
	javaConfig := gazelleConfig.Exts[languageName].(javaconfig.Configs)
	require.Equal(t, "install_maven.json", javaConfig[""].MavenInstallFile())
}

func TestKotlinModuleNameDirective(t *testing.T) {
	c, _, configurers := testConfig(t)
	parentFile, err := rule.LoadData("service/BUILD.bazel", "service", []byte(`# gazelle:java_kotlin_module_name service_module
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, configurer := range configurers {
		configurer.Configure(c, "service", parentFile)
	}

	childFile := rule.EmptyFile("service/child/BUILD.bazel", "service/child")
	for _, configurer := range configurers {
		configurer.Configure(c, "service/child", childFile)
	}

	configs := c.Exts[languageName].(javaconfig.Configs)
	if got := configs["service/child"].KotlinModuleName(); got != "service_module" {
		t.Fatalf("child KotlinModuleName() = %q, want service_module", got)
	}
}
