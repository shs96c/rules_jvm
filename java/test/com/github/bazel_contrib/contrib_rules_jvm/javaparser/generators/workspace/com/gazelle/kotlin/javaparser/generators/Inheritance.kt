package workspace.com.gazelle.kotlin.javaparser.generators

import com.example.BaseClass
import com.example.interfaces.Clickable

// Class extending a base class
class SimpleInheritance : com.example.BaseClass()

// Class implementing interfaces
class InterfaceImpl : com.example.interfaces.Clickable, com.example.interfaces.Focusable

// Class with both superclass and interfaces
class MixedInheritance : com.example.BaseClass(), com.example.interfaces.Clickable

// Class with delegation
class DelegatedImpl(delegate: com.example.interfaces.Clickable) : 
    com.example.interfaces.Clickable by delegate

// Class with multiple delegations
class MultipleDelegations(
    clickDelegate: com.example.interfaces.Clickable,
    focusDelegate: com.example.interfaces.Focusable
) : com.example.interfaces.Clickable by clickDelegate,
    com.example.interfaces.Focusable by focusDelegate

// Using imported classes
class ImportedInheritance : BaseClass(), Clickable

// Object extending a class
object Singleton : com.example.BaseClass()

// Object implementing interface
object Handler : com.example.interfaces.EventHandler

// Nested class with inheritance
class Outer {
  class Inner : com.example.nested.InnerBase()
  
  object NestedObject : com.example.interfaces.Serializer
}

// Enum implementing interface
enum class Status : com.example.interfaces.Comparable {
  ACTIVE,
  INACTIVE
}

// Sealed class hierarchy
sealed class Result : com.example.sealed.BaseResult()
data class Success(val data: String) : Result()
data class Failure(val error: String) : Result()

// Interface extending other interfaces
interface MyInterface : com.example.interfaces.Base, com.example.interfaces.Extended
