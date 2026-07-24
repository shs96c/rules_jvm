package com.squareup.cash.common.persistence

@Retention(AnnotationRetention.RUNTIME)
@Target(AnnotationTarget.CLASS)
annotation class ShardedEntity

@ShardedEntity
class Repo {
  fun find(): String = "x"
}
