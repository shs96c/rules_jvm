package workspace.com.gazelle.kotlin.javaparser.generators

import com.example.annotations.CustomAnnotation
import com.example.validation.NotNull

// Simple annotation on class
@com.example.annotations.Deprecated
class AnnotatedClass

// Multiple annotations
@com.example.annotations.Entity
@com.example.annotations.Table(name = "users")
class MultipleAnnotations

// Annotation with class literal argument
@com.example.serialization.JsonSerializable(using = com.example.serializers.CustomSerializer::class)
class WithClassLiteral

// Multiple class literals in annotation
@com.example.annotations.Throws(
  com.example.exceptions.IOException::class,
  com.example.exceptions.IllegalArgumentException::class
)
fun throwingFunction() {
  // ...
}

// Annotations on properties
class AnnotatedProperties {
  @com.example.validation.NotNull
  @com.example.validation.Size(min = 1, max = 100)
  val name: String = ""
  
  @com.example.serialization.JsonProperty("user_id")
  val userId: Long = 0
  
  @com.example.annotations.Transient
  private val temp: Int = 0
}

// Annotations on functions
@com.example.testing.Test
fun testFunction() {
  // ...
}

@com.example.web.RequestMapping(path = "/api/users", method = "GET")
@com.example.web.ResponseBody
fun handleRequest() {
  // ...
}

// Annotations on parameters
fun processUser(
  @com.example.validation.NotNull user: String,
  @com.example.web.RequestParam("id") userId: Long
) {
  // ...
}

// Annotation with imported class
@CustomAnnotation(value = com.example.data.User::class)
class WithImportedAnnotation

// Nested annotation arguments
@com.example.meta.Metadata(
  annotations = [
    com.example.annotations.Entity::class,
    com.example.annotations.Serializable::class
  ]
)
class NestedAnnotations

// Annotation on constructor
class AnnotatedConstructor @com.example.annotations.Inject constructor(
  @com.example.annotations.Named("primary") val service: String
)

// Annotation on getter/setter
class PropertyAnnotations {
  @get:com.example.validation.NotNull
  @set:com.example.validation.Valid
  var value: String = ""
}

// Annotation on type
fun genericFunction(
  param: @com.example.annotations.NonNull String
): @com.example.annotations.NonNull Int {
  return 0
}

// Annotation on file
@file:com.example.annotations.JvmName("AnnotationsFile")
package workspace.com.gazelle.kotlin.javaparser.generators

// Repeatable annotations
@com.example.validation.Constraint(type = "required")
@com.example.validation.Constraint(type = "unique")
class RepeatedAnnotations

// Annotation on type parameter
class GenericAnnotated<@com.example.annotations.TypeConstraint T>

// Extension function with annotation
@com.example.extensions.Extension
fun String.customExtension() {
  // ...
}

// Object with annotations
@com.example.annotations.Singleton
object AnnotatedObject

// Enum with annotations
enum class AnnotatedEnum {
  @com.example.annotations.Default
  ACTIVE,
  
  @com.example.annotations.Deprecated
  INACTIVE
}

// Data class with annotations
@com.example.serialization.Serializable
data class AnnotatedDataClass(
  @com.example.validation.NotNull val id: Long,
  @com.example.serialization.JsonProperty("user_name") val name: String
)

// Annotation with array of classes
@com.example.meta.SupportedTypes(
  types = [
    com.example.types.TypeA::class,
    com.example.types.TypeB::class,
    com.example.types.TypeC::class
  ]
)
class ArrayOfClasses

// Annotation with nested annotation
@com.example.meta.Container(
  nested = com.example.meta.Nested(value = com.example.data.Data::class)
)
class NestedAnnotationClass
