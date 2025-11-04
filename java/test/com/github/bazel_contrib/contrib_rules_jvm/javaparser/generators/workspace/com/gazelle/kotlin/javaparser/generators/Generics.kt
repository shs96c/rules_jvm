package workspace.com.gazelle.kotlin.javaparser.generators

import com.example.Container
import com.example.data.Result

// Simple generic type arguments in properties
class GenericProperties {
  val list: List<com.example.data.User> = emptyList()
  val map: Map<String, com.example.data.Profile> = emptyMap()
  val set: Set<com.example.nested.Item> = emptySet()
  
  // Nested generics
  val nested: List<Map<String, com.example.data.User>> = emptyList()
  
  // Multiple type parameters
  val pair: Pair<com.example.data.User, com.example.data.Profile> = TODO()
  
  // Imported class as type argument
  val container: Container<com.example.data.Result> = TODO()
}

// Generic function parameters
fun processUsers(users: List<com.example.data.User>) {
  // ...
}

fun transformData(
  input: Map<String, com.example.data.Input>,
  transformer: (com.example.data.Input) -> com.example.data.Output
): List<com.example.data.Output> {
  return emptyList()
}

// Generic class with bounds
class GenericClass<T : com.example.base.Entity> {
  fun process(item: T): List<T> = emptyList()
}

// Multiple type parameter bounds
class MultipleConstraints<
  T : com.example.interfaces.Comparable,
  R : com.example.interfaces.Serializable
> {
  val items: List<T> = emptyList()
  val results: Set<R> = emptySet()
}

// Generic function with bounds
fun <T : com.example.base.Entity> process(item: T): T = item

fun <T> transform(
  item: T,
  processor: com.example.Processor<T>
): Result<T> where T : com.example.interfaces.Validatable = TODO()

// Star projections and variance
fun handleWildcards(
  producer: com.example.Producer<out com.example.data.User>,
  consumer: com.example.Consumer<in com.example.data.Profile>,
  invariant: com.example.Handler<com.example.data.Event>
) {
  // ...
}

// Nullable generic types
val nullableList: List<com.example.data.User?> = emptyList()
val nullableMap: Map<String, com.example.data.Profile?> = emptyMap()

// Array with generics
val array: Array<com.example.data.Item> = emptyArray()

// Generic extension function
fun <T : com.example.base.Entity> T.validate(): Boolean = true

// Local variable with generics
fun localGenerics() {
  val local: List<com.example.data.User> = emptyList()
  val result: Result<com.example.data.Output> = TODO()
}

// Return types with generics
fun getUsers(): List<com.example.data.User> = emptyList()
fun getMapping(): Map<String, com.example.data.Profile> = emptyMap()

// Generic delegation
class DelegatedContainer<T>(
  private val delegate: MutableList<T>
) : MutableList<T> by delegate

// Type alias usage (if we support it later)
typealias UserList = List<com.example.data.User>
typealias UserMap = Map<String, com.example.data.Profile>
