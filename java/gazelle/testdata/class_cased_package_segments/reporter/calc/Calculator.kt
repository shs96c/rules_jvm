package com.example.calc

import com.example.engines.PaymentMethods.AggregationHelper
import com.example.engines.PaymentMethods.AvailableFilters.QueryEngine
import com.example.engines.PaymentMethods.ReportQueryVariables
import com.example.engines.PaymentMethods.getPaginationCursor
import com.example.engines.PaymentMethods.reachedAggregationLimit

class Calculator {
  private val helper: AggregationHelper? = null
  private val engine: QueryEngine? = null

  fun limited(): Boolean = reachedAggregationLimit()

  fun cursor(): String = getPaginationCursor()

  fun variables(): ReportQueryVariables = ReportQueryVariables(limit = 10)
}
