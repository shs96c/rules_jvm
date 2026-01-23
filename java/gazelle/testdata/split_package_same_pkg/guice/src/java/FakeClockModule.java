package com.example.time;

// NOTE: This file is in the SAME Java package as Clock and FakeClock,
// but in a DIFFERENT Bazel package.
//
// Since Clock and FakeClock are in the same Java package, no import is needed.
// This is where the Gazelle bug manifests: the Java parser doesn't track
// same-package type references, so Gazelle doesn't know this file depends
// on Clock and FakeClock.

public class FakeClockModule {
    // Uses Clock and FakeClock from the same Java package (no import needed)
    private final Clock clock;
    private final FakeClock fakeClock;

    public FakeClockModule() {
        this.fakeClock = new FakeClock();
        this.clock = fakeClock;
    }

    public Clock getClock() {
        return clock;
    }
}
