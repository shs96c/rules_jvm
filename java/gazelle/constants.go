package gazelle

// This file declares two unrelated families of private-attribute keys. They
// look superficially similar (both name "the Java packages/classes a rule
// provides") but they target different audiences and carry different value
// types. Keeping them separate is deliberate.
//
// ┌──────────────────────────┬──────────────────────────────────────────────┐
// │ packagesKey / classesKey │ ProvidedPackagesKey / ProvidedClassesKey     │
// ├──────────────────────────┼──────────────────────────────────────────────┤
// │ Internal to this plugin. │ Public cross-plugin contract.                │
// │ Set by the Java plugin   │ Set by OTHER Gazelle plugins (e.g.           │
// │ on its own generated     │ rules_wire) on rules they generate.          │
// │ rules.                   │                                              │
// │                          │                                              │
// │ Value type:              │ Value type: []string.                        │
// │ []types.ResolvableJava-  │ Plain strings — chosen so external           │
// │ Package — encodes        │ producers do NOT need a Go dependency on     │
// │ test/test-suite flags;   │ contrib_rules_jvm/java/gazelle/private/types │
// │ requires importing our   │ to populate them. Decoupling is the whole    │
// │ private/types package.   │ point of this contract.                      │
// │                          │                                              │
// │ Read by Imports() and    │ Scraped by indexOtherGenJavaSymbols() from   │
// │ registered in Gazelle's  │ args.OtherGen during GenerateRules; folded   │
// │ RuleIndex so other Java  │ into otherGenPackageIndex / classExportCache │
// │ rules can find them.     │ for the resolver to consult.                 │
// │                          │                                              │
// │ Underscore prefix per    │ No underscore — they're a documented,        │
// │ Gazelle convention for   │ stable, public name shape that upstream      │
// │ "private to this plugin, │ plugins are expected to set verbatim.        │
// │ never written to BUILD". │                                              │
// └──────────────────────────┴──────────────────────────────────────────────┘
//
// Do not collapse these into a single set of keys: an external plugin can't
// construct a types.ResolvableJavaPackage without taking on our internal
// dependencies, and the scraper can't type-assert against a struct without
// forcing that same dependency. The two paths must stay distinct.

// packagesKey is the private-attribute key used by the Java plugin on its OWN
// generated rules to record which Java packages they provide. The value is
// []types.ResolvableJavaPackage. Imports() reads this key and registers the
// packages in Gazelle's RuleIndex so other Java rules can resolve imports to
// them.
const packagesKey = "_java_packages"

// classesKey is the private-attribute key used by the Java plugin on its OWN
// generated rules to record the bare outer class names they provide (a list
// of strings). Used internally for split-package class-level resolution.
const classesKey = "_java_classes"

// ProvidedClassesKey and ProvidedPackagesKey form the cross-plugin OtherGen
// contract that lets upstream Gazelle plugins (e.g. rules_wire) advertise the
// Java symbols their generated rules provide WITHOUT taking a Go dependency on
// contrib_rules_jvm. The Java plugin scrapes args.OtherGen for these
// attributes during its own GenerateRules pass and folds the symbols into its
// in-memory indexes; the resolver then routes Java imports to the producing
// target.
//
// Both attributes hold a []string. Producers must register their language
// extension BEFORE the Java extension in the gazelle_binary so their rules
// show up in args.OtherGen when the Java plugin runs.
//
//   - ProvidedClassesKey: fully-qualified class names (e.g. "com.example.User").
//     Used for class-level disambiguation when multiple producers advertise
//     the same Java package.
//   - ProvidedPackagesKey: Java package names (e.g. "com.example"). Used for
//     the package-level fast path.
//
// These names are part of the public contract — do not rename them without
// coordinating with downstream plugins.
const (
	ProvidedClassesKey  = "java_provided_classes"
	ProvidedPackagesKey = "java_provided_packages"
)
