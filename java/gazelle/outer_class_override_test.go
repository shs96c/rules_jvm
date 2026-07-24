package gazelle

import (
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

func TestFindClassRuleWithOverride(t *testing.T) {
	for name, tc := range map[string]struct {
		directives string
		want       string
	}{
		"exact takes precedence": {
			directives: `# gazelle:resolve java com.example.TenderProtos @outer//:outer
# gazelle:resolve java com.example.TenderProtos.Tender @exact//:exact
`,
			want: "@exact//:exact",
		},
		"outer is fallback": {
			directives: `# gazelle:resolve java com.example.TenderProtos @outer//:outer
`,
			want: "@outer//:outer",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, _, configurers := testConfig(t)
			f, err := rule.LoadData("BUILD.bazel", "", []byte(tc.directives))
			if err != nil {
				t.Fatal(err)
			}
			for _, configurer := range configurers {
				configurer.Configure(c, "", f)
			}

			className, err := types.ParseClassName("com.example.TenderProtos.Tender")
			if err != nil {
				t.Fatal(err)
			}
			got, found := findClassRuleWithOverride(c, *className)
			if !found {
				t.Fatal("findClassRuleWithOverride() did not find an override")
			}
			if got.String() != tc.want {
				t.Fatalf("findClassRuleWithOverride() = %s, want %s", got, tc.want)
			}
		})
	}
}
