package workspace.com.gazelle.kotlin.javaparser.generators

class CallChainReceivers {
  fun demo() {
    // Call chain: the receiver of the final `.Bar()` selector is `Value.foo(1)`,
    // whose text contains a `.` but is not a fully-qualified identifier (parens).
    // Must not be recorded as a class reference.
    Value.foo(1).Bar()

    // Constructor call through a single-segment package: must be recorded.
    val buffer = okio.Buffer()

    // A local value used as receiver of a class-like selector is not a package.
    val localReceiver = Value
    localReceiver.Buffer()
  }

  fun withParameter(parameterReceiver: Value) {
    parameterReceiver.Buffer()
  }

  fun withLambda(values: List<Value>) {
    values.forEach { it.Buffer() }
  }
}
