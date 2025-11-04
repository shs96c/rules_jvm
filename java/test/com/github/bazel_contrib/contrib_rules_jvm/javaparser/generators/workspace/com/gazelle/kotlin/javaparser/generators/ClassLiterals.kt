package workspace.com.gazelle.kotlin.javaparser.generators

import com.example.MyCustomClass
import com.example.data.UserProfile

class ClassLiterals {
  fun testClassLiterals() {
    // KClass references
    val kclass1 = com.example.MyCustomClass::class
    val kclass2 = com.example.data.UserProfile::class
    val kclass3 = java.util.HashMap::class
    
    // Java class references
    val javaClass1 = com.example.MyCustomClass::class.java
    val javaClass2 = com.example.data.UserProfile::class.java
    
    // In collections
    val classes = listOf(
      com.example.MyCustomClass::class,
      com.example.data.UserProfile::class,
      java.util.ArrayList::class
    )
    
    // As function arguments
    processClass(com.example.MyCustomClass::class)
    processJavaClass(com.example.data.UserProfile::class.java)
    
    // Imported classes
    val importedKClass = MyCustomClass::class
    val importedJavaClass = UserProfile::class.java
  }
  
  private fun processClass(clazz: kotlin.reflect.KClass<*>) {
    println(clazz.simpleName)
  }
  
  private fun processJavaClass(clazz: Class<*>) {
    println(clazz.name)
  }
}
