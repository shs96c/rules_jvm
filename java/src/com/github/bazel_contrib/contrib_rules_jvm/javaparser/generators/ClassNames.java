package com.github.bazel_contrib.contrib_rules_jvm.javaparser.generators;

/**
 * Shared heuristics for deciding whether a simple name looks like a class name (PascalCase) vs. a
 * constant, type parameter, or other identifier.
 */
final class ClassNames {

  private ClassNames() {}

  /**
   * Returns true if the given simple name looks like a PascalCase class name.
   *
   * <p>Returns false for: empty strings, names that don't start with an uppercase letter, single
   * uppercase characters (likely type parameters), and ALL_CAPS_SNAKE_CASE constants.
   */
  static boolean isLikelyClassName(String name) {
    if (name.isEmpty()) {
      return false;
    }
    if (!firstLetterIsUppercase(name)) {
      return false;
    }
    // Check that there is at least one lowercase letter after the first character.
    // This rejects single uppercase chars (type parameters like T) and
    // ALL_CAPS / SCREAMING_SNAKE_CASE constants.
    for (int i = 1; i < name.length(); i++) {
      char c = name.charAt(i);
      if (Character.isLetter(c) && Character.isLowerCase(c)) {
        return true;
      }
    }
    return false;
  }

  /**
   * Returns true for a class-like component inside an import path.
   *
   * <p>All-uppercase class names such as {@code DSL} are accepted only when another path component
   * follows them, which distinguishes {@code DSL.noCondition} from a terminal constant import.
   */
  static boolean isLikelyClassNameInImportPath(String name, boolean hasTrailingComponent) {
    if (isLikelyClassName(name)) {
      return true;
    }
    if (!hasTrailingComponent || name.length() < 2 || !firstLetterIsUppercase(name)) {
      return false;
    }
    for (int i = 0; i < name.length(); i++) {
      char c = name.charAt(i);
      if (c == '_' || (Character.isLetter(c) && !Character.isUpperCase(c))) {
        return false;
      }
    }
    return true;
  }

  private static boolean firstLetterIsUppercase(String value) {
    for (int i = 0; i < value.length(); i++) {
      char c = value.charAt(i);
      if (Character.isLetter(c)) {
        return Character.isUpperCase(c);
      }
    }
    return false;
  }
}
