package kotlin

import (
	"testing"

	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
)

func TestIsStdLib(t *testing.T) {
	tests := map[string]bool{
		"":                   false,
		"kotlin":             true,
		"kotlin.math":        true,
		"kotlin.collections": true,
		"java.lang":          false,
		"com.example":        false,
		// kotlin.test ships as a separate artifact, so it is not stdlib.
		"kotlin.test":       false,
		"kotlin.test.junit": false,
		// kotlin.testing is a distinct package that still lives under kotlin, so the
		// kotlin.test exclusion must not match it on a bare string prefix.
		"kotlin.testing": true,
		// kotlin.metadata ships as a separate artifact, so it is not stdlib.
		"kotlin.metadata":     false,
		"kotlin.metadata.jvm": false,
		// kotlin.metadatas is a distinct package that still lives under kotlin, so the
		// kotlin.metadata exclusion must not match it on a bare string prefix.
		"kotlin.metadatas": true,
		// kotlin.reflect ships as a separate artifact, so it is not stdlib.
		"kotlin.reflect":      false,
		"kotlin.reflect.full": false,
		// kotlin.reflects is a distinct package that still lives under kotlin, so the
		// kotlin.reflect exclusion must not match it on a bare string prefix.
		"kotlin.reflects": true,
	}

	for pkg, want := range tests {
		t.Run(pkg, func(t *testing.T) {
			if got := IsStdlib(types.NewPackageName(pkg)); got != want {
				t.Errorf("IsStdLib() = %v, want %v", got, want)
			}
		})
	}
}
