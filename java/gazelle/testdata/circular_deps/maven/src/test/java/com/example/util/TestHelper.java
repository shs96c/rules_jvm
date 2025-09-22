package com.example.util;

import com.example.service.UserService;

public class TestHelper {
    
    public static String sanitize(String input) {
        if (input == null) return "";
        return input.trim().toLowerCase();
    }
    
    // Test code that uses production service (creates circular dependency)
    public static void validateService() {
        UserService service = new UserService();
        service.processUser("test");
    }
}
