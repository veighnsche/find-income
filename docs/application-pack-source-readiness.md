# First application pack — existing source readiness

Historical evidence and design notes. The fixed-board discovery implementation and its execution plan were removed on 24 September 2026; see [current remaining work](remaining-work.md). References below to discovery tasks are not current implementation instructions.

Coordinator check, 23 September 2026. This prepares I15 without claiming pack implementation or live acceptance. The current Typst source compiles with installed Typst 0.15.1; output was written only to /private/tmp/jobseek-i15-readiness.pdf. Existing CV/PDF and personal material were not changed or copied into the repository.

| Existing local source | SHA-256 | Intended use |
| --- | --- | --- |
| cv-vince-liem.typ (project root) | `e9643864392f2aff7f900a82714a8feb573f636c24c62e7a169c29de41bc9a57` | Current CV layout and truthful employment/project separation |
| cv-vince-liem.md (project root) | `eaf82b8442ac51007b83397279d7f16e0f4a8547bd63f340253ad8263a9ddf61` | Editable CV content cross-check |
| github-evidence-review.md (project root) | `4bf7467279e2440d7a1e870274726055bd1d33550e7272a7731aa568e0f6c4f3` | Pinned public repository evidence and claim limitations |
| portfolio-case-studies.md (project root) | `67f5355ae303479a363b0040fe9b18732fce308612791f0666bbf7250a33d1c0` | Project examples; internal notes must not enter a published pack |

The sources are local inputs under /Users/vince/Projects/find-income, outside the dashboard Git repository. Import only the material needed by the selected role into private application storage. Preserve exact source/version references; repository descriptions are not proof of deployment, test success, commercial adoption or paid specialist tenure.

The current CV separates recent personal Go/Rust/Python/platform work from prior paid work. Historical frontend work stays historical and must not become the desired role. PHP appears only in the dated 2017–2018 Iuppiter history. Do not introduce freelance labels. The case-study file contains an explicitly internal notes section that must be excluded from employer-facing output.

Compilation command actually run:

```sh
typst compile --root /Users/vince/Projects/find-income /Users/vince/Projects/find-income/cv-vince-liem.typ /private/tmp/jobseek-i15-readiness.pdf
```

I15 still needs selected-role requirements, approved evidence selection, immutable pack material/answers/destination, private storage, generated PDF inspection and contextual correction. It can use a sourced fixture while I14 discovery finishes; I16 must then validate a real lead-to-pack result. Successful compilation alone proves none of that workflow.
