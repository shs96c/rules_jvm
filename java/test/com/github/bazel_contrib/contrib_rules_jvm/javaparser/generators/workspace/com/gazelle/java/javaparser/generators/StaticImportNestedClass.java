package workspace.com.gazelle.java.javaparser.generators;

import static com.example.Outer.Inner;
import static com.example.Outer.android_process;

public class StaticImportNestedClass {
    // Static imports make nested type owners available by their bare names.
    Inner value;
    android_process.Builder processBuilder;
}
