"""Public Starlark API of mutrim."""

load("//bazel:mutation_test.bzl", _mutation_test = "mutation_test")

mutation_test = _mutation_test
