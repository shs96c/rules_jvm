package com.github.bazel_contrib.contrib_rules_jvm.javaparser.generators;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.gazelle.java.javaparser.v0.Package;
import com.gazelle.java.javaparser.v0.ParseJavaPackagesRequest;
import com.gazelle.java.javaparser.v0.ParseJavaPackagesResponse;
import com.gazelle.java.javaparser.v0.ParsePackageRequest;
import io.grpc.stub.StreamObserver;
import java.nio.file.Files;
import java.nio.file.Path;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class GrpcServerTest {
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
