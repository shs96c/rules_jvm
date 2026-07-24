package com.example.consumer;

import com.example.shared.Shared;

// Shared is declared by both provider_a and provider_b (an ambiguous in-repo
// package split), so it can only be resolved through the existing dependency
// on provider_a, which must be kept rather than dropped as unresolvable.
public class Consumer {
  public String sharedName() {
    return Shared.name();
  }
}
