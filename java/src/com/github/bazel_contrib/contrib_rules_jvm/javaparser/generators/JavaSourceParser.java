package com.github.bazel_contrib.contrib_rules_jvm.javaparser.generators;

import static com.github.bazel_contrib.contrib_rules_jvm.javaparser.generators.ClassNames.isLikelyClassName;
import static com.github.bazel_contrib.contrib_rules_jvm.javaparser.generators.ClassNames.isLikelyClassNameInImportPath;

import com.github.javaparser.JavaParser;
import com.github.javaparser.ParseResult;
import com.github.javaparser.ParserConfiguration;
import com.github.javaparser.ast.CompilationUnit;
import com.github.javaparser.ast.ImportDeclaration;
import com.github.javaparser.ast.Node;
import com.github.javaparser.ast.body.AnnotationMemberDeclaration;
import com.github.javaparser.ast.body.BodyDeclaration;
import com.github.javaparser.ast.body.CallableDeclaration;
import com.github.javaparser.ast.body.CompactConstructorDeclaration;
import com.github.javaparser.ast.body.EnumConstantDeclaration;
import com.github.javaparser.ast.body.MethodDeclaration;
import com.github.javaparser.ast.body.Parameter;
import com.github.javaparser.ast.body.TypeDeclaration;
import com.github.javaparser.ast.body.VariableDeclarator;
import com.github.javaparser.ast.expr.AnnotationExpr;
import com.github.javaparser.ast.expr.Expression;
import com.github.javaparser.ast.expr.FieldAccessExpr;
import com.github.javaparser.ast.expr.MethodCallExpr;
import com.github.javaparser.ast.expr.ObjectCreationExpr;
import com.github.javaparser.ast.expr.TypePatternExpr;
import com.github.javaparser.ast.nodeTypes.NodeWithAnnotations;
import com.github.javaparser.ast.nodeTypes.NodeWithExtends;
import com.github.javaparser.ast.nodeTypes.NodeWithImplements;
import com.github.javaparser.ast.nodeTypes.NodeWithTypeParameters;
import com.github.javaparser.ast.type.Type;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import javax.tools.JavaFileObject;
import javax.tools.SimpleJavaFileObject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/** Extracts dependency metadata without starting a compiler task for each package. */
public final class JavaSourceParser {
  private static final Logger logger = LoggerFactory.getLogger(JavaSourceParser.class);
  private final JavaParser parser =
      new JavaParser(
          new ParserConfiguration()
              .setLanguageLevel(ParserConfiguration.LanguageLevel.BLEEDING_EDGE)
              .setStoreTokens(false)
              .setDetectOriginalLineSeparator(false)
              .setPreprocessUnicodeEscapes(true));

  public ParsedPackageData parseClasses(Path directory, List<String> files) throws IOException {
    ParsedPackageData data = new ParsedPackageData();
    for (String file : files) {
      Path source = directory.resolve(file);
      data.merge(parseSource(source.toString(), Files.readString(source)));
    }
    return data;
  }

  public ParsedPackageData parseClasses(List<? extends JavaFileObject> files) throws IOException {
    ParsedPackageData data = new ParsedPackageData();
    for (JavaFileObject file : files) {
      data.merge(parseSource(file.getName(), file.getCharContent(true).toString()));
    }
    return data;
  }

  private ParsedPackageData parseSource(String name, String content) throws IOException {
    ParseResult<CompilationUnit> result = parser.parse(content);
    if (!result.isSuccessful()) {
      // JavaParser does not support all valid Java syntax, including method-local enums.
      logger.warn("JavaParser could not parse {}; using javac", name);
      JavaFileObject source =
          new SimpleJavaFileObject(Path.of(name).toUri(), JavaFileObject.Kind.SOURCE) {
            @Override
            public CharSequence getCharContent(boolean ignoreEncodingErrors) {
              return content;
            }
          };
      return new ClasspathParser().parseClasses(List.of(source));
    }
    return new Scanner().scanUnit(result.getResult().orElseThrow());
  }

  private static final class Scanner {
    private final ParsedPackageData data = new ParsedPackageData();
    private final Map<String, String> imports = new HashMap<>();
    private final Set<String> excluded = new HashSet<>(ClasspathParser.JAVA_LANG_TYPES);
    private final Deque<Node> context = new ArrayDeque<>();
    private String packageName;

    ParsedPackageData scanUnit(CompilationUnit unit) {
      packageName = unit.getPackageDeclaration().map(p -> p.getNameAsString()).orElse(null);
      if (packageName != null) {
        data.packages.add(packageName);
      }
      unit.findAll(TypeDeclaration.class).forEach(type -> excluded.add(type.getNameAsString()));
      for (TypeDeclaration<?> type : unit.getTypes()) {
        imports.put(type.getNameAsString(), qualify(type.getNameAsString()));
      }
      unit.getPackageDeclaration().ifPresent(this::scanChildren);
      unit.getImports().forEach(this::scanImport);
      unit.getTypes().forEach(this::scan);
      return data;
    }

    private void scanImport(ImportDeclaration declaration) {
      String name = declaration.getNameAsString();
      if (declaration.isStatic()) {
        if (declaration.isAsterisk()) {
          data.usedTypes.add(name);
          imports.put("*", name + ".*");
        } else {
          int dot = name.lastIndexOf('.');
          data.usedTypes.add(name.substring(0, dot));
          String member = name.substring(dot + 1);
          imports.put(member, name);
          if (isLikelyClassName(member)) {
            data.usedTypes.add(name);
          }
        }
      } else if (declaration.isAsterisk()) {
        String last = name.substring(name.lastIndexOf('.') + 1);
        if (isLikelyClassNameInImportPath(last, true)) {
          data.usedTypes.add(name);
        } else {
          data.usedPackagesWithoutSpecificTypes.add(name);
        }
      } else {
        imports.put(name.substring(name.lastIndexOf('.') + 1), name);
        data.usedTypes.add(name);
      }
    }

    private void scan(Node node) {
      if (node instanceof ObjectCreationExpr creation) {
        scanCreation(creation);
        return;
      }
      if (node instanceof EnumConstantDeclaration constant) {
        scanEnumConstant(constant);
        return;
      }
      boolean scope =
          node instanceof TypeDeclaration<?>
              || node instanceof CallableDeclaration<?>
              || node instanceof AnnotationMemberDeclaration
              || node instanceof CompactConstructorDeclaration;
      if (node instanceof TypeDeclaration<?> type && context.isEmpty()) {
        data.declaredTypes.add(qualify(type.getNameAsString()));
      }
      if (scope) {
        context.addLast(node);
      }
      if (node instanceof TypeDeclaration<?> declaration) {
        typeParameters(declaration);
        if (declaration instanceof NodeWithExtends<?> extended) {
          extended.getExtendedTypes().forEach(this::type);
        }
        if (declaration instanceof NodeWithImplements<?> implemented) {
          implemented.getImplementedTypes().forEach(this::type);
        }
        annotations(declaration);
        declaration.getAnnotations().forEach(a -> classData().annotations.add(annotationName(a)));
      } else if (node instanceof CallableDeclaration<?> callable) {
        typeParameters(callable);
        annotations(callable);
        callable.getThrownExceptions().forEach(this::type);
        String name = "<init>";
        if (callable instanceof MethodDeclaration method) {
          name = method.getNameAsString();
          Set<String> returns = type(method.getType());
          if (!method.isPrivate()) {
            data.exportedTypes.addAll(returns);
          }
          if (name.equals("main")
              && method.isPublic()
              && method.isStatic()
              && method.getType().isVoidType()) {
            data.mainClasses.add(nestedName());
          }
        }
        noteMethodAnnotations(callable, name);
      } else if (node instanceof CompactConstructorDeclaration constructor) {
        annotations(constructor);
        noteMethodAnnotations(constructor, "<init>");
      } else if (node instanceof AnnotationMemberDeclaration member) {
        annotations(member);
        data.exportedTypes.addAll(type(member.getType()));
        noteMethodAnnotations(member, member.getNameAsString());
      } else if (node instanceof VariableDeclarator variable) {
        type(variable.getType());
        Node parent = variable.getParentNode().orElse(null);
        if (parent instanceof NodeWithAnnotations<?> annotated) {
          annotations(annotated);
          if (directlyInClass()) {
            noteFieldAnnotations(annotated, variable.getNameAsString());
          }
        }
      } else if (node instanceof Parameter parameter) {
        type(parameter.getType());
        annotations(parameter);
        if (directlyInClass()) {
          noteFieldAnnotations(parameter, parameter.getNameAsString());
        }
      } else if (node instanceof com.github.javaparser.ast.expr.ClassExpr literal) {
        type(literal.getType());
      } else if (node instanceof MethodCallExpr call) {
        call.getScope().ifPresent(this::methodReceiver);
      } else if (node instanceof TypePatternExpr pattern) {
        type(pattern.getType());
      }
      scanChildren(node);
      if (scope) {
        context.removeLast();
      }
    }

    private void scanCreation(ObjectCreationExpr creation) {
      type(creation.getType());
      creation.getChildNodes().stream()
          .filter(child -> !(child instanceof BodyDeclaration<?>))
          .forEach(this::scan);
      creation
          .getAnonymousClassBody()
          .ifPresent(
              members -> {
                context.addLast(creation);
                members.forEach(this::scan);
                context.removeLast();
              });
    }

    private void scanEnumConstant(EnumConstantDeclaration constant) {
      Node owner = context.peekLast();
      if (owner instanceof TypeDeclaration<?> declaration) {
        name(declaration.getNameAsString());
      }
      annotations(constant);
      noteFieldAnnotations(constant, constant.getNameAsString());
      constant.getArguments().forEach(this::scan);
      constant.getAnnotations().forEach(this::scan);
      if (!constant.getClassBody().isEmpty()) {
        context.addLast(constant);
        constant.getClassBody().forEach(this::scan);
        context.removeLast();
      }
    }

    private void scanChildren(Node node) {
      node.getChildNodes().forEach(this::scan);
    }

    private void typeParameters(Node node) {
      if (node instanceof NodeWithTypeParameters<?> parameterised) {
        parameterised
            .getTypeParameters()
            .forEach(
                parameter -> {
                  excluded.add(parameter.getNameAsString());
                  parameter.getTypeBound().forEach(this::type);
                });
      }
    }

    private void annotations(NodeWithAnnotations<?> node) {
      node.getAnnotations().forEach(annotation -> name(annotation.getNameAsString()));
    }

    private String annotationName(AnnotationExpr annotation) {
      String name = annotation.getNameAsString();
      return imports.getOrDefault(name, name);
    }

    private void noteMethodAnnotations(NodeWithAnnotations<?> node, String method) {
      node.getAnnotations()
          .forEach(
              annotation ->
                  classData()
                      .perMethodAnnotations
                      .computeIfAbsent(method, unused -> new TreeSet<>())
                      .add(annotationName(annotation)));
    }

    private void noteFieldAnnotations(NodeWithAnnotations<?> node, String field) {
      node.getAnnotations()
          .forEach(
              annotation ->
                  classData()
                      .perFieldAnnotations
                      .computeIfAbsent(field, unused -> new TreeSet<>())
                      .add(annotationName(annotation)));
    }

    private Set<String> type(Type type) {
      if (!type.getAnnotations().isEmpty()) {
        return Set.of();
      }
      if (type.isArrayType()) {
        return type(type.asArrayType().getComponentType());
      }
      if (!type.isClassOrInterfaceType()) {
        return Set.of();
      }
      var reference = type.asClassOrInterfaceType();
      Set<String> base = name(reference.getNameWithScope());
      if (reference.getTypeArguments().isEmpty()) {
        return base;
      }
      // The exported types of a generic return are its arguments, not its raw container.
      Set<String> arguments = new TreeSet<>();
      reference.getTypeArguments().get().forEach(argument -> arguments.addAll(type(argument)));
      return arguments;
    }

    private Set<String> name(String name) {
      return TypeNameResolver.resolve(name, imports, packageName, excluded)
          .map(
              resolved -> {
                data.usedTypes.add(resolved);
                return Set.of(resolved);
              })
          .orElseGet(Set::of);
    }

    private void methodReceiver(Expression expression) {
      String terminal;
      if (expression instanceof FieldAccessExpr access) {
        if (access.getScope().isNameExpr()) {
          String root = access.getScope().asNameExpr().getNameAsString();
          if (!root.isEmpty()
              && !imports.containsKey(root)
              && Character.isLowerCase(root.codePointAt(0))) {
            return;
          }
        }
        terminal = access.getNameAsString();
      } else if (expression.isNameExpr()) {
        terminal = expression.asNameExpr().getNameAsString();
      } else {
        return;
      }
      if (imports.containsKey(terminal) || isLikelyClassName(terminal)) {
        name(expression.toString());
      }
    }

    private boolean directlyInClass() {
      Node owner = context.peekLast();
      return owner instanceof TypeDeclaration<?>
          || owner instanceof ObjectCreationExpr
          || owner instanceof EnumConstantDeclaration;
    }

    private String nestedName() {
      List<String> names = new ArrayList<>();
      for (Node owner : context) {
        if (owner instanceof TypeDeclaration<?> declaration) {
          names.add(declaration.getNameAsString());
        } else if (owner instanceof ObjectCreationExpr
            || owner instanceof EnumConstantDeclaration) {
          names.add("");
        }
      }
      return String.join(".", names);
    }

    private String qualify(String name) {
      return packageName == null || packageName.isEmpty() ? name : packageName + "." + name;
    }

    private PerClassData classData() {
      return data.perClassData.computeIfAbsent(qualify(nestedName()), unused -> new PerClassData());
    }
  }
}
