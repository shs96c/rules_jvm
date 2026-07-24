package kotlin

import (
	"github.com/bazel-contrib/rules_jvm/java/gazelle/private/types"
)

var kotlinStdLibPrefix = types.NewPackageName("kotlin")

// kotlinTestPrefix is in the `kotlin` namespace but ships in a separate artifact
// (org.jetbrains.kotlin:kotlin-test), not kotlin-stdlib, so it must resolve to a maven
// dependency rather than being assumed on the classpath.
var kotlinTestPrefix = types.NewPackageName("kotlin.test")

// kotlinMetadataPrefix is in the `kotlin` namespace but ships in a separate artifact
// (org.jetbrains.kotlin:kotlin-metadata-jvm), not kotlin-stdlib, so it must resolve to
// a maven dependency rather than being assumed on the classpath.
var kotlinMetadataPrefix = types.NewPackageName("kotlin.metadata")

// kotlinReflectPrefix is in the `kotlin` namespace but reflection extensions ship in a
// separate artifact (org.jetbrains.kotlin:kotlin-reflect), not kotlin-stdlib, so they
// must resolve to a maven dependency rather than being assumed on the classpath.
var kotlinReflectPrefix = types.NewPackageName("kotlin.reflect")

func IsStdlib(imp types.PackageName) bool {
	if types.PackageNamesHasPrefix(imp, kotlinTestPrefix) ||
		types.PackageNamesHasPrefix(imp, kotlinMetadataPrefix) ||
		types.PackageNamesHasPrefix(imp, kotlinReflectPrefix) {
		return false
	}
	return types.PackageNamesHasPrefix(imp, kotlinStdLibPrefix)
}
