package com.gazelle.kotlin.javaparser.generators

class FullyQualifiedTopLevelFunctions {
  fun call(valueReceiver: Any) {
    com.example.helpers.doThing()
    com.example.Helper.doThing()
    valueReceiver.helpers.doThing()
    lineItems.discountList.map()
    javaClass.classLoader.getResourceAsStream("resource")
    builder.data.build()
    data.items.map()
  }
}
