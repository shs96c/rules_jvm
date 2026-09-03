package workspace.com.gazelle.kotlin.javaparser.generators

import com.example.api.Outer.SignRequest
import com.example.alias.Outer as proto
import com.example.deep.Outer.Middle.Deep as Namespace

class ImportAliases {
  fun nested(value: SignRequest): SignRequest = value

  fun outerAlias(value: proto.Nested): proto.Nested = value

  fun deepAlias(value: Namespace.Child): Namespace.Child = value

  fun expressionAlias() = proto.Nested

  @Throws(Exception::class)
  @Suppress("UNUSED_PARAMETER")
  fun jvmDefaults(
    classValue: Class<*>,
    math: Math,
    system: System,
    thread: Thread,
  ): Void? = null
}
