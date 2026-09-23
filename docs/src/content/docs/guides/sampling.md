---
title: Sampling
description: "Run a fraction of the mutants."
---

`run -sample 0.2` trades accuracy for time: it runs a random ~20% of the mutants, selected
by a hash of their ID and `-seed`, and reports the rest `SKIPPED`, so they count towards no
score and the score is computed over the sample (Gopinath et al., ICSE 2015/2016, TSE
2017: operator selection and stratified sampling are at best marginally better than uniform
random sampling, and a ~10% sample loses under a point of score). `totals.sampled`
records the fraction actually kept.

Mutant IDs are content hashes, and the hash covers the seed, so the selection needs no
bookkeeping: with the same `-seed`, every shard of a run and every `-previous` run keeps
the same mutants. `minimize` takes the kills as they are — a kill of a kept mutant is
still a kill — but the requirements the sample left out may have changed the verdicts, so
a `redundant` test is weaker under sampling.
