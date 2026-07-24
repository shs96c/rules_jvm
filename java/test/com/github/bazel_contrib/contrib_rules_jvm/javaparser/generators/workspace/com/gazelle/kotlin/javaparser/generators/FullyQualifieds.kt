package workspace.com.gazelle.kotlin.javaparser.generators

class FullyQualifieds {
  fun fn() {
    workspace.com.gazelle.java.javaparser.generators.DeleteBookRequest()
    workspace.com.gazelle.java.javaparser.utils.Printer.print()

    val response: workspace.com.gazelle.java.javaparser.generators.DeleteBookResponse =
        workspace.com.gazelle.java.javaparser.factories.Factory.create()

    // instance methods shouldn't get detected as classes (e.g. this.foo).
    this.foo.bar()

    // Anonymous variables in lambdas shouldn't be picked up as variable names - visitVariable sees
    // them as variables with null types.
    someList.map { x -> x.toString() }

    java.util.ArrayList<String>().map { y -> y.toString() }

    okio.Buffer()

    val localReceiver = this
    localReceiver.Buffer()

    listOf(this).map { "${it.Buffer}" }

    doReturn(spyGrpcCall).`when`(transactionwriterServiceClient).ProcessPaymentPlanEligibilityEvent()
    verify(transactionwriterServiceClient, org.mockito.Mockito.never()).ProcessPaymentPlanEligibilityEvent()

    this.BLAH = "beep"
    this.BEEP_BOOP = "baz"
  }

  fun localReceiver(parameterReceiver: Any) {
    parameterReceiver.Buffer()
  }

  private fun privateFn(privateArg: com.example.PrivateArg): String {
    // Top level functions should result in adding the package to the used packages list.
    return com.example.externalPrivateFn(privateArg)
  }
}
