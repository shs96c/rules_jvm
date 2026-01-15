package com.example.hello.notworld.withhelpers;

import static org.junit.Assert.assertEquals;

import org.junit.jupiter.api.Test;
import com.example.hello.notworld.NotWorld;
import com.example.hello.notworld.justhelpersinmodule.withdirectory.Model;
import com.example.hello.notworld.justhelpersinmodule.withdirectory.Helper;

public class NotWorldTest {
  @Test
  public void notWorld() {
    assertEquals(Helper.getExpectation(), Model.NOT_WORLD);
    assertEquals(Helper.getExpectation(), NotWorld.NOT_WORLD);
  }
}
