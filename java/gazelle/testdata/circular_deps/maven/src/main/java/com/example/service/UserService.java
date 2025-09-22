package com.example.service;

import com.example.util.TestHelper;

public class UserService {
    
    public String processUser(String name) {
        // Production code that uses test helper (bad practice but happens in real codebases)
        return TestHelper.sanitize(name);
    }
}
