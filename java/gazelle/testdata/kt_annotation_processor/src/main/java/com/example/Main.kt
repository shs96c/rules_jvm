package com.example

import com.google.auto.value.AutoValue

fun main() {
    val pig = Animal.create("pig", 4)
    val chicken = Animal.create("chicken", 2)
    println("Checking if $pig has same legs as $chicken: ${pig.numberOfLegs() == chicken.numberOfLegs()}")
}

@AutoValue
abstract class Animal {
    companion object {
        @JvmStatic
        fun create(name: String, numberOfLegs: Int): Animal {
            return AutoValue_Animal(name, numberOfLegs)
        }
    }

    abstract fun name(): String
    abstract fun numberOfLegs(): Int
}
