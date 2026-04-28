# `doc.go` Template (dKV Style)

Use this structure for every exported package.

## Rules

1. Start with `// Package <name> ...` in the first line.
2. Describe responsibility, not implementation details.
3. Use compact bullet lists for focus areas and key components.
4. Mention related packages only when this improves navigation.
5. Keep wording stable across packages to create a predictable reading flow.

## Template

```go
// Package <name> provides <short responsibility statement>.
// It defines <primary abstraction> and supports <main use-cases>.
//
// The package focuses on:
//   - <focus point 1>
//   - <focus point 2>
//   - <focus point 3>
//
// Key Components:
//
//   - <TypeOrInterfaceA>: <what it does and why it exists>.
//   - <TypeOrInterfaceB>: <what it does and why it exists>.
//   - <FunctionOrSubsystemC>: <what it does and why it exists>.
//
// Related Packages:
//
//   - <path/to/pkg1>: <relationship>.
//   - <path/to/pkg2>: <relationship>.
package <name>
```

## Minimal Variant

For very small packages, keep a shorter version:

```go
// Package <name> provides <single responsibility> for the backend system.
// It contains <main types/functions> used by <callers>.
package <name>
```

## Backend Package Coverage

Use `doc.go` at least for:

1. Root command/server package.
2. HTTP transport/handler packages.
3. Service/use-case packages.
4. Store/repository/data-access packages.
5. Shared domain types and validation packages.
