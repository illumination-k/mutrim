---
title: Kubernetes (Argo Workflows)
description: "The schemata pipeline as an Argo Workflows DAG, fanned out over packages and shards."
---

[`deploy/argo/mutation-pipeline.yaml`](https://github.com/illumination-k/mutrim/blob/main/deploy/argo/mutation-pipeline.yaml)
is a `WorkflowTemplate` that runs the non-Bazel pipeline (the one `mise run dogfood` runs
locally) on a cluster: one pod builds per package, one pod runs per shard, and the reports
join into per-package and suite-wide results.

```text
checkout ─┬─ package(a) ─┐
          ├─ package(b) ─┼─ suite
          └─ ...        ─┘

package(pkg) = prepare ─┬─ shard 0 ─┬─ summarize
                        ├─ shard 1 ─┤
                        └─ ...     ─┘
```

| Step        | Runs                                                                                 | Writes                                                               |
| ----------- | ------------------------------------------------------------------------------------ | -------------------------------------------------------------------- |
| `checkout`  | git clone, `go mod download`, `go install .../cmd/mutrim@<mutrim-version>`           | the workspace (sources, module cache, `mutrim`) every step reuses    |
| `prepare`   | `mutrim gen -schemata -blocks`, `go test -c -overlay`                                | `mutants.json`, `blocks.json` and the schemata test binary           |
| `shard`     | `mutrim run` with `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS`                           | `reports/<pkg>/<shard>.json`                                         |
| `summarize` | `mutrim minimize` and `mutrim report` (Stryker JSON, HTML) over the package's shards | `results/<pkg>/`                                                     |
| `suite`     | one `mutrim minimize -matrix` over every package's reports                           | `results/suite/`: a test judged by what it covers and kills anywhere |

```bash
kubectl apply -f deploy/argo/mutation-pipeline.yaml
argo submit --watch --from workflowtemplate/mutrim-mutation-pipeline \
  -p repo=https://github.com/you/yours.git -p revision=main \
  -p packages='["pkg/a", "pkg/b"]' -p shards=8 -p jobs=4
```

| Parameter        | Default                                         | Meaning                                                        |
| ---------------- | ----------------------------------------------- | -------------------------------------------------------------- |
| `repo`           | this repository                                 | git URL of the module to mutate (its `go.mod` at the root)     |
| `revision`       | `main`                                          | branch, tag or commit                                          |
| `packages`       | `["criteria", "minimize", "mutator", "runner"]` | package directories, a JSON list                               |
| `shards`         | `4`                                             | shards per package                                             |
| `jobs`           | `2`                                             | `mutrim run -jobs`, and the CPUs each shard pod requests       |
| `sample`, `seed` | `0`, `0`                                        | `mutrim run -sample` / `-seed` ([Sampling](../sampling/))      |
| `mutrim-version` | `main`                                          | version of mutrim to `go install`                              |
| `image`          | `golang:1.27`                                   | image of every step; it needs the Go toolchain the module asks |

The steps pass their files through the default artifact repository, which must be
S3-compatible (S3, MinIO, GCS through its S3 API): the shard reports are key-only
artifacts under `<workflow name>/reports/<pkg>/`, read back as one directory per package
and one for the suite. The results sit under `<workflow name>/results/`. Mutants are
sharded by ID, so the shards of a package share nothing but the prepared build and a shard
that dies on a lost node is retried once.
