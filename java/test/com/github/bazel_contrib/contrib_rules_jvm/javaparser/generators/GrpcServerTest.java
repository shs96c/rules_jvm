package com.github.bazel_contrib.contrib_rules_jvm.javaparser.generators;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.gazelle.java.javaparser.v0.CacheIdentityRequest;
import com.gazelle.java.javaparser.v0.CacheIdentityResponse;
import com.gazelle.java.javaparser.v0.Package;
import com.gazelle.java.javaparser.v0.ParseJavaPackagesRequest;
import com.gazelle.java.javaparser.v0.ParseJavaPackagesResponse;
import com.gazelle.java.javaparser.v0.ParsePackageRequest;
import io.grpc.stub.StreamObserver;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicInteger;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class GrpcServerTest {
  @Test
  void cacheIdentityFingerprintsTheRuntimeClasspath(@TempDir Path workspace) {
    TimeoutHandler timeout = new TimeoutHandler(0);
    try (var service = new GrpcServer.GrpcService(workspace, timeout)) {
      var first = new RecordingObserver<CacheIdentityResponse>();
      var second = new RecordingObserver<CacheIdentityResponse>();
      service.cacheIdentity(CacheIdentityRequest.getDefaultInstance(), first);
      service.cacheIdentity(CacheIdentityRequest.getDefaultInstance(), second);
      assertTrue(first.completed);
      assertEquals(64, first.value.getIdentity().length());
      assertEquals(first.value, second.value);
    } finally {
      timeout.cancelOutstandingAndStopScheduling();
    }
  }

  @Test
  void cacheIdentityChangesWithParserDependenciesAndRuntime(@TempDir Path workspace)
      throws Exception {
    Path parser = workspace.resolve("parser.jar");
    Path dependency = workspace.resolve("dependency.jar");
    Files.writeString(parser, "parser implementation");
    Files.writeString(dependency, "dependency v1");
    var files = List.of(parser, dependency);
    String initial = GrpcServer.parserIdentity(files, "runtime-v1");
    assertEquals(initial, GrpcServer.parserIdentity(files, "runtime-v1"));
    assertNotEquals(initial, GrpcServer.parserIdentity(files, "runtime-v2"));
    Files.writeString(dependency, "dependency v2");
    assertNotEquals(initial, GrpcServer.parserIdentity(files, "runtime-v1"));
    Files.writeString(dependency, "dependency v1");
    Files.writeString(parser, "changed parser");
    assertNotEquals(initial, GrpcServer.parserIdentity(files, "runtime-v1"));
    assertThrows(
        java.io.IOException.class,
        () -> GrpcServer.parserIdentity(List.of(workspace), "runtime-v1"));
  }

  @Test
  void kotlinParserIsLazyReusedAndClosed(@TempDir Path workspace) {
    var parser = mock(KtParser.class);
    var creations = new AtomicInteger();
    var files = List.of(workspace.resolve("Example.kt"));
    when(parser.parseClasses(files)).thenReturn(new ParsedPackageData());
    TimeoutHandler timeout = new TimeoutHandler(0);
    try (var service =
        new GrpcServer.GrpcService(
            workspace,
            timeout,
            () -> {
              creations.incrementAndGet();
              return parser;
            })) {
      service.parsePackage(ParsePackageRequest.getDefaultInstance(), new RecordingObserver<>());
      assertEquals(0, creations.get());
      var request = ParsePackageRequest.newBuilder().addFiles("Example.kt").build();
      service.parsePackage(request, new RecordingObserver<>());
      service.parsePackage(request, new RecordingObserver<>());
      assertEquals(1, creations.get());
      verify(parser, times(2)).parseClasses(files);
    } finally {
      timeout.cancelOutstandingAndStopScheduling();
    }
    verify(parser).close();
  }

  @Test
  void closingWaitsForAnActiveKotlinRequest(@TempDir Path workspace) throws Exception {
    var parser = mock(KtParser.class);
    var entered = new CountDownLatch(1);
    var release = new CountDownLatch(1);
    var files = List.of(workspace.resolve("Example.kt"));
    when(parser.parseClasses(files))
        .thenAnswer(
            invocation -> {
              entered.countDown();
              assertTrue(release.await(10, TimeUnit.SECONDS));
              return new ParsedPackageData();
            });
    TimeoutHandler timeout = new TimeoutHandler(0);
    try (var service = new GrpcServer.GrpcService(workspace, timeout, () -> parser);
        var executor = Executors.newFixedThreadPool(2)) {
      var request = ParsePackageRequest.newBuilder().addFiles("Example.kt").build();
      var parsing = executor.submit(() -> service.parsePackage(request, new RecordingObserver<>()));
      try {
        assertTrue(entered.await(10, TimeUnit.SECONDS));
        var closing = executor.submit(service::close);
        assertThrows(TimeoutException.class, () -> closing.get(100, TimeUnit.MILLISECONDS));
        verify(parser, never()).close();
        release.countDown();
        closing.get(10, TimeUnit.SECONDS);
        parsing.get(10, TimeUnit.SECONDS);
      } finally {
        release.countDown();
      }
    } finally {
      timeout.cancelOutstandingAndStopScheduling();
    }
    verify(parser).close();
  }

  @Test
  void batchPreservesMetadataAndIsolatesParseFailures(@TempDir Path workspace) throws Exception {
    Files.createDirectories(workspace.resolve("good"));
    Files.createDirectories(workspace.resolve("bad"));
    Files.writeString(
        workspace.resolve("good/Example.java"),
        """
        package sample;
        @Deprecated
        class Example {
          @Deprecated public example.Result result() { return new example.Result(); }
          public static void main(String[] args) {}
        }
        """);
    Files.writeString(workspace.resolve("bad/Broken.java"), "class Broken { void method( }");
    ParsePackageRequest good =
        ParsePackageRequest.newBuilder().setRel("good").addFiles("Example.java").build();
    ParsePackageRequest bad =
        ParsePackageRequest.newBuilder().setRel("bad").addFiles("Broken.java").build();
    TimeoutHandler timeout = new TimeoutHandler(0);
    try {
      var service = new GrpcServer.GrpcService(workspace, timeout);
      var ordinary = new RecordingObserver<Package>();
      service.parsePackage(good, ordinary);
      var batch = new RecordingObserver<ParseJavaPackagesResponse>();
      service.parseJavaPackages(
          ParseJavaPackagesRequest.newBuilder().addPackages(good).addPackages(bad).build(), batch);

      assertTrue(batch.completed);
      assertEquals(ordinary.value, batch.value.getPackagesOrThrow("good"));
      assertEquals(1, batch.value.getPackagesCount());
      assertTrue(batch.value.containsErrors("bad"));
    } finally {
      timeout.cancelOutstandingAndStopScheduling();
    }
  }

  private static final class RecordingObserver<T> implements StreamObserver<T> {
    T value;
    boolean completed;

    @Override
    public void onNext(T value) {
      this.value = value;
    }

    @Override
    public void onError(Throwable error) {
      throw new AssertionError(error);
    }

    @Override
    public void onCompleted() {
      completed = true;
    }
  }
}
