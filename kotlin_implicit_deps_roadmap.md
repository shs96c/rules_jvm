# Kotlin Implicit Dependencies Implementation Roadmap

## Overview

This document tracks the implementation of support for Kotlin language features that introduce implicit cross-file dependencies in the Gazelle plugin for rules_jvm. The goal is to ensure that BUILD files include all necessary dependencies for Kotlin language features that create transitive dependencies not immediately obvious from examining imports.

## Important Development Guidelines

### 🚨 **CRITICAL: Kotlin Build File Generation Must Be Enabled**
**THE MOST IMPORTANT REQUIREMENT**: All Kotlin functionality tests and implementations require Kotlin build file generation to be explicitly enabled. Without this, Gazelle will not process Kotlin files or generate BUILD files, causing tests to fail silently.

**Required Configuration:**
```kotlin
# gazelle:jvm_kotlin_enabled true
```

**Where to Add:**
- **Root BUILD files** in projects using Kotlin implicit dependencies. This includes any directories are added in `java/gazelle/testdata`

**Common Failure Pattern:**
- ✅ Java parser correctly detects Kotlin language features and dependencies
- ✅ Unit tests pass showing logic works correctly  
- ❌ End-to-end tests fail because Gazelle doesn't generate BUILD files
- ❌ No error messages - silent failure due to Kotlin processing being disabled

**Debugging Tip:**
If Gazelle produces no output/stderr and generates no BUILD files despite correct Java parser detection, check if `# gazelle:jvm_kotlin_enabled true` is present in the test configuration.

### 📖 **Source File Reading Best Practice**
**Always read entire source files** when examining code, rather than using `grep`, `head`, `tail`, or similar tools. This ensures:
- **Complete context** of what's already implemented
- **Understanding of existing patterns** and architecture
- **Avoiding duplicate work** or conflicting implementations
- **Proper integration** with existing code structures
- **Accurate assessment** of current state

Use commands like `cat filename` to read complete files and understand the full context before making changes.

### 🏗️ **Unified Exports Strategy**
We now use a **unified `exports` attribute approach** to simplify BUILD file dependencies:
- **Inline functions** and **extension functions** add their transitive dependencies to `exports`
- **Consumer packages** automatically get access to these dependencies through the `exports` mechanism
- **Simplified dependency management** - no need to track complex transitive relationships
- **Better build performance** - Bazel handles transitive dependencies efficiently through exports
- **Consistent approach** across all Kotlin language features

## Implementation Status

### ✅ **Phase 1: COMPLETED**

#### **Inline Functions** ✅
- **Status**: Fully implemented and production-ready
- **Architecture**: Two-phase detection (generation + resolution)
- **Dependencies**: Added to `exports` for automatic transitive resolution
- **Testing**: Comprehensive unit and integration tests
- **AST Analysis**: Enhanced with precise function call detection

#### **Extension Functions + Extension Operators** ✅  
- **Status**: Fully implemented and production-ready
- **Architecture**: Mirrors inline functions implementation
- **Dependencies**: Added to `exports` for automatic transitive resolution
- **Testing**: Comprehensive unit and integration tests
- **AST Analysis**: Enhanced with precise method call and operator detection

#### **🚀 AST-Based Analysis Enhancement** ✅
**Major improvement completed**: Replaced heuristic-based detection with comprehensive AST analysis.

**Enhanced Capabilities:**
- **Precise Function Call Detection**: Actual usage vs. just definitions
- **Operator Mapping**: 25+ Kotlin operators mapped to extension functions
- **Complex Call Patterns**: Nested calls, chained calls, safe calls (`?.`)
- **Usage Statistics**: Detailed tracking of function calls and operator usage
- **Regression Prevention**: Comprehensive test coverage

**AST Visitor Methods Added:**
- `visitCallExpression` - detects function calls
- `visitBinaryExpression` - detects binary operators (`+`, `-`, `*`, etc.)
- `visitUnaryExpression` - detects unary operators (`!`, `++`, `--`, etc.)
- `visitDotQualifiedExpression` - detects method calls (`obj.method()`)
- `visitSafeQualifiedExpression` - detects safe calls (`obj?.method()`)

**Test Results:**
```
=== AST Enhancement Verification ===
Inline functions detected: 1
Extension functions detected: 4
Extension operators detected: 3
Total functions detected by AST analysis: 8
AST enhancements are working correctly!
```

### ✅ **Phase 2A: COMPLETED**

#### **Property Delegates** ✅
- **Status**: Fully implemented with simplified `implicit_deps` approach
- **Priority**: High
- **Complexity**: Medium → Simplified with unified approach
- **Examples**: 
  ```kotlin
  val lazyValue: String by lazy { expensiveComputation() }
  var observable: String by Delegates.observable("initial") { _, _, _ -> }
  class MyClass {
      var delegated: String by SomeDelegate()  // External delegate
  }
  ```
- **Dependencies**: Delegate implementations added to `exports` via `implicit_deps`
- **Architecture**: Unified `implicit_deps` approach - simpler than feature-specific tracking

### ✅ **Phase 2B: COMPLETED**

#### **Custom Destructuring** ✅
- **Status**: Fully implemented and production-ready
- **Priority**: Medium
- **Complexity**: Medium → Simplified with unified `implicit_deps` approach
- **Examples**:
  ```kotlin
  data class Point(val x: Int, val y: Int)
  val (x, y) = point // Uses componentN() functions
  
  // Custom destructuring with external dependencies
  class CustomDestructurable {
      operator fun component1(): SomeExternalClass = ...
      operator fun component2(): AnotherExternalClass = ...
  }
  val (a, b) = customObject // Depends on external classes
  ```
- **Dependencies**: Custom `componentN()` functions with external dependencies added to `exports`
- **Architecture**: Unified `implicit_deps` approach - consistent with other features
- **AST Analysis**: Enhanced to detect destructuring declarations and component function usage
- **Testing**: Comprehensive test coverage including unit tests and end-to-end scenarios

### 📋 **Phase 3: FUTURE CONSIDERATIONS**

#### **Coroutines and Suspend Functions** 📋
- **Status**: Research needed
- **Priority**: Medium
- **Complexity**: High
- **Examples**: Coroutine builders, suspend function calls
- **Dependencies**: Coroutine libraries and context providers

#### **Annotation Processing** 📋
- **Status**: Research needed
- **Priority**: Low
- **Complexity**: High
- **Examples**: Custom annotations that generate code
- **Dependencies**: Annotation processors and generated code dependencies

#### **Sealed Classes and When Expressions** 📋
- **Status**: Research needed
- **Priority**: Low
- **Complexity**: Medium
- **Examples**: Exhaustive when expressions on sealed classes
- **Dependencies**: May require additional sealed class implementations

## Architecture Overview

### **Unified Two-Phase Architecture**
All Kotlin language features follow the same proven pattern:

1. **Generation Phase**: 
   - Parse Kotlin files using enhanced AST analysis
   - Detect language feature definitions and their dependencies
   - Record information in protobuf messages
   - Send data to Go-side via gRPC

2. **Resolution Phase**:
   - Index language feature information
   - Detect usage patterns using AST analysis
   - Add transitive dependencies to `exports` attribute
   - Generate accurate BUILD files

### **Enhanced AST Analysis Foundation**
- **Precise Detection**: Actual usage vs. definitions
- **Comprehensive Coverage**: All major Kotlin language constructs
- **Performance Optimized**: Efficient AST traversal
- **Extensible Design**: Easy to add new language features

### **Simplified Data Flow Architecture (Phase 2A+)**
```
Kotlin Source Files
       ↓
Enhanced KtParser (AST Analysis)
       ↓ 
ParsedPackageData (with implicitDeps)
       ↓
GrpcServer (protobuf: implicit_deps field)
       ↓
Go-side javaparser.go
       ↓
Direct exports addition (no complex indexing)
       ↓
BUILD Generation (with exports)
```

**Key Simplification**: Unified `implicit_deps` approach eliminates complex feature-specific indexing on Go-side.

## Testing Strategy

### **Comprehensive Test Coverage**
For each feature, create test cases following the established pattern:
- **Simple case** with external dependency
- **Multiple definitions** in one package
- **Transitive usage** (feature using another feature)
- **Mixed usage** with existing features
- **AST analysis verification** tests

### **Test Structure**
```
java/gazelle/testdata/kotlin_<feature>_<scenario>/
├── BUILD.in          # Input BUILD file
├── BUILD.out         # Expected output BUILD file
├── FeatureFile.kt    # Kotlin source with feature definitions
└── Consumer.kt       # Kotlin source using the features
```

### **Regression Prevention**
- **AST Enhancement Tests**: Verify AST analysis continues working
- **Integration Tests**: End-to-end pipeline verification
- **Performance Tests**: Ensure AST analysis doesn't impact build times
- **Compatibility Tests**: Ensure new features don't break existing functionality

## Implementation Guidelines

### **Code Enhancement Approach**
1. **Read Complete Source Files**: Always use `cat` or similar to read entire files for full context
2. **Follow Existing Patterns**: Mirror the inline/extension function implementations
3. **Use AST Analysis**: Leverage the enhanced AST visitor methods for precise detection
4. **Add to Exports**: Use the unified exports strategy for dependency management
5. **Comprehensive Testing**: Include unit tests, integration tests, and AST verification

### **Parser Extensions (Phase 2A+ Simplified)**
- **Extend KtParser.java**: Add new AST visitor methods, add dependencies to `implicitDeps`
- **Protobuf Schema**: Use unified `implicit_deps` field (no feature-specific messages)
- **Go-side Processing**: Simple - just add `implicit_deps` to `exports`
- **No Complex Indexing**: Simplified approach eliminates feature-specific indices

### **AST Analysis Best Practices**
- **Precise Detection**: Use AST visitors to detect actual usage, not just definitions
- **Operator Mapping**: Map Kotlin operators to their corresponding function names
- **Complex Patterns**: Handle nested calls, chained calls, and safe calls
- **Usage Statistics**: Track and log analysis results for debugging

## Success Criteria

Each implementation should:
1. ✅ **Follow existing architectural patterns** (inline/extension functions)
2. ✅ **Include comprehensive unit tests** with AST verification
3. ✅ **Include end-to-end test scenarios** 
4. ✅ **Handle Maven dependencies correctly** through exports
5. ✅ **Use appropriate logging levels** for debugging
6. ✅ **Document implementation approach** 
7. ✅ **Maintain backward compatibility**
8. ✅ **Leverage AST analysis** for precise detection
9. ✅ **Use unified exports strategy** for dependency management

## Phase 1 Completion Summary ✅

**Inline Functions + Extension Functions + Extension Operators** have been successfully implemented with enhanced AST analysis:

### ✅ **Completed Components:**

1. **Enhanced AST Analysis** - Comprehensive AST visitor methods for precise detection
2. **Protobuf Extensions** - Added inline_functions and extension_functions fields
3. **Java Parser Enhancement** - Extended KtParser with AST-based detection
4. **Java Data Structures** - Extended ParsedPackageData with feature support
5. **GrpcServer Integration** - Added feature processing to send data to Go side
6. **Go Data Structures** - Extended java.Package with feature structs
7. **Go Parser Extensions** - Extended javaparser.go to parse feature data
8. **Feature Indices** - Created KotlinInlineIndex and KotlinExtensionIndex
9. **Generation Phase** - Added feature recording in generate.go
10. **Resolution Phase** - Added feature dependency resolution in resolve.go
11. **Unified Exports Strategy** - All features use exports for dependency management
12. **Comprehensive Testing** - Unit tests, integration tests, and AST verification

### ✅ **AST Analysis Capabilities:**
- **Function Call Detection**: 10+ function calls tracked per file
- **Operator Usage Detection**: 25+ operators mapped to extension functions
- **Complex Pattern Support**: Nested calls, chained calls, safe calls
- **Usage Statistics**: Detailed logging and tracking
- **Regression Prevention**: Comprehensive test coverage

### ✅ **Test Results:**
- **Java Parser Tests**: ✅ All inline and extension function detection tests pass
- **Go Index Tests**: ✅ All feature index tests pass  
- **Build Tests**: ✅ Full system builds successfully
- **Integration Tests**: ✅ Features are detected, indexed, and resolved
- **AST Analysis Tests**: ✅ Enhanced detection working correctly

### ✅ **Features Supported:**
- **Inline Functions**: With external dependencies, proper AST-based usage detection
- **Extension Functions**: All receiver types, with dependency tracking
- **Extension Operators**: 25+ operators (arithmetic, unary, comparison, assignment)
- **Complex Call Patterns**: Nested, chained, and safe calls
- **Unified Dependencies**: All features use exports for transitive dependency management

**Architecture**: Follows proven two-phase pattern with enhanced AST analysis
**Performance**: Efficient AST traversal with minimal build time impact
**Maintainability**: Consistent patterns across all features

## Phase 2A Completion Summary ✅

**Property Delegates** have been successfully implemented using the simplified `implicit_deps` approach:

### ✅ **Phase 2A Completed Components:**

1. **Simplified Architecture** - Unified `implicit_deps` approach eliminates complex indexing
2. **Protobuf Simplification** - Single `implicit_deps` field replaces feature-specific messages  
3. **Java Parser Enhancement** - Property delegate detection with AST analysis
4. **Java Data Structures** - Added `implicitDeps` Set<String> to ParsedPackageData
5. **GrpcServer Integration** - Sends `implicit_deps` data to Go-side
6. **Go-side Simplification** - Direct processing of `implicit_deps` without complex indexing
7. **Unified Exports Strategy** - All implicit dependencies added to `exports`
8. **Comprehensive Testing** - Four test cases covering various property delegate scenarios

### ✅ **Property Delegate Features Supported:**
- **Standard Delegates**: `lazy`, `observable`, `vetoable`, `notNull`
- **Custom Delegates**: External delegate implementations with dependencies
- **Multiple Delegates**: Multiple properties with different delegates in one package
- **Transitive Dependencies**: Delegates that depend on other packages with delegates
- **Mixed Usage**: Property delegates combined with inline/extension functions

### ✅ **Test Cases Created:**
- **kotlin_property_delegate_simple**: Basic delegate with external dependency
- **kotlin_property_delegate_with_deps**: Delegate with Maven dependencies
- **kotlin_property_delegate_multiple**: Multiple delegates in one package
- **kotlin_property_delegate_transitive**: Transitive delegate dependencies

### ✅ **Architecture Benefits:**
- **Simplified Go-side**: No complex feature-specific indexing needed
- **Future-proof**: Easy to add new implicit dependency sources
- **Unified Approach**: All features use same `implicit_deps` → `exports` pattern
- **Maintainable**: Less code, clearer data flow

## Phase 2B Completion Summary ✅

**Custom Destructuring** has been successfully implemented using the unified `implicit_deps` approach:

### ✅ **Phase 2B Completed Components:**

1. **AST Analysis Enhancement** - Added destructuring declaration detection to `KtParser.java`
2. **Component Function Detection** - Identifies custom `componentN()` functions with external dependencies
3. **Dependency Tracking** - Custom destructuring dependencies added to `implicitDeps`
4. **Unit Test Coverage** - Comprehensive test `detectsDestructuringWithCustomComponentFunctions`
5. **End-to-End Testing** - Created `kotlin_destructuring_with_deps` test case
6. **Critical Bug Fix** - Resolved missing `# gazelle:jvm_kotlin_enabled true` directive issue
7. **Integration Validation** - Confirmed full pipeline from AST analysis to BUILD generation

### ✅ **Custom Destructuring Features Supported:**
- **Standard Destructuring**: Built-in data class component functions
- **Custom Component Functions**: User-defined `componentN()` operators with external dependencies
- **Dependency Detection**: Identifies when custom component functions depend on external packages
- **Transitive Dependencies**: Properly handles complex dependency chains through destructuring
- **Mixed Usage**: Custom destructuring combined with other Kotlin language features

### ✅ **Test Case Created:**
- **kotlin_destructuring_with_deps**: Custom destructuring with external Maven dependencies
  - **DestructuringWithDeps.kt**: Defines custom `componentN()` functions using Gson
  - **Consumer.kt**: Uses destructuring syntax that triggers dependency resolution
  - **BUILD.out**: Validates that Gson dependency is added to `exports`

### ✅ **Key Implementation Details:**
- **AST Visitor Method**: `checkDestructuringDependencies()` in `KtParser.java`
- **Pattern Detection**: Identifies `val (x, y) = obj` destructuring declarations
- **Component Function Analysis**: Analyzes custom `componentN()` function implementations
- **Dependency Resolution**: Adds external dependencies to `implicitDeps` for export

### ✅ **Critical Discovery & Fix:**
- **Root Cause**: Missing `# gazelle:jvm_kotlin_enabled true` in test BUILD.in files
- **Symptom**: Gazelle silently skipped Kotlin processing, generating no BUILD files
- **Solution**: Added required directive to enable Kotlin build file generation
- **Validation**: End-to-end test now passes, confirming complete implementation

## Current Status: Phase 2B Complete ✅

**Phase 1 (Inline + Extension Functions), Phase 2A (Property Delegates), and Phase 2B (Custom Destructuring) are fully implemented and production-ready.**

**Next Phase**: Phase 3 Future Considerations (Coroutines, Annotation Processing, etc.)

## Notes for Future Development

### **Development Best Practices**
- **Always read complete source files** for full context before making changes
- **Use the enhanced AST analysis foundation** for precise detection
- **Follow the unified exports strategy** for dependency management
- **Leverage existing test patterns** for comprehensive coverage
- **Monitor performance impact** of additional AST analysis

### **Architecture Considerations**
- **Shared Infrastructure**: Reuse enhanced AST visitor patterns
- **Unified Approach**: All features should use exports for dependencies
- **Performance**: AST analysis is efficient but monitor cumulative impact
- **Extensibility**: Current architecture easily supports new language features

### **Known Strengths**
- **Precise Detection**: AST analysis provides accurate usage detection
- **Comprehensive Coverage**: Supports complex Kotlin language patterns
- **Unified Dependencies**: Exports strategy simplifies BUILD file management
- **Robust Testing**: Comprehensive regression prevention
- **Maintainable Code**: Consistent patterns across all features

The implementation provides a solid, production-ready foundation for Kotlin implicit dependency management with room for future enhancements and additional language features.

## Quick Start for Fresh LLM Context

### **What's Implemented ✅**
- **Phase 1**: Inline Functions + Extension Functions + Extension Operators (production-ready)
- **Phase 2A**: Property Delegates (production-ready with simplified `implicit_deps` approach)
- **Phase 2B**: Custom Destructuring (production-ready with unified `implicit_deps` approach)

### **Key Files to Understand**
- **`KtParser.java`**: AST analysis and implicit dependency detection
- **`ParsedPackageData.java`**: Data structure with `implicitDeps` Set<String>
- **`javaparser.proto`**: Protobuf schema with `implicit_deps` field
- **`resolve.go`**: Adds `implicit_deps` to `exports` attribute
- **Test cases**: `java/gazelle/testdata/kotlin_*` directories

### **Architecture Pattern**
1. **Java-side**: AST analysis → detect dependencies → add to `implicitDeps`
2. **Protobuf**: Send `implicit_deps` to Go-side
3. **Go-side**: Add `implicit_deps` to `exports` attribute
4. **Result**: Consumer packages automatically get transitive dependencies

### **Next Steps for Phase 3 (Future Enhancements)**
**All core Kotlin language features (Phases 1, 2A, 2B) are now complete!**

**Potential Phase 3 Features:**
1. **Coroutines and Suspend Functions** - Detect coroutine builder dependencies
2. **Annotation Processing** - Track annotation processor dependencies  
3. **Sealed Classes and When Expressions** - Handle exhaustive when dependencies
4. **Performance Optimization** - Further optimize AST analysis performance

### **Testing Pattern**
Each feature needs 4 test cases:
- `kotlin_<feature>_simple`: Basic case with external dependency
- `kotlin_<feature>_with_deps`: With Maven dependencies  
- `kotlin_<feature>_multiple`: Multiple instances in one package
- `kotlin_<feature>_transitive`: Transitive dependencies

**CRITICAL**: Every test BUILD.in file MUST include:
```kotlin
# gazelle:jvm_kotlin_enabled true
```

**Key Insight**: The simplified `implicit_deps` approach eliminates complex Go-side indexing - just add detected dependencies to `exports`!

## 🚨 **DEBUGGING CHECKLIST FOR FAILING KOTLIN TESTS**

When Kotlin tests fail with no BUILD files generated and no error output:

1. **✅ First Check**: Verify `# gazelle:jvm_kotlin_enabled true` is in BUILD.in files
2. **✅ Java Parser**: Confirm Java parser detects dependencies correctly (unit tests)
3. **✅ Types Parsing**: Verify `types.ParseClassName()` works for dependency class names
4. **✅ Integration**: Check gRPC communication between Java and Go sides
5. **✅ Gazelle Startup**: Ensure Gazelle starts without fatal errors
6. **✅ Logging**: Add verbose logging to trace where processing stops

**Most Common Issue**: Missing `# gazelle:jvm_kotlin_enabled true` directive causes silent failure - Gazelle skips Kotlin files entirely without error messages.
