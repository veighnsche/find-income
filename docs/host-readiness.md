# Existing hosts — read-only I12 prerequisite check

Observed by coordinator on 23 September 2026 using existing SSH aliases with batch authentication and strict host-key checking. No account login, service deployment, package installation or configuration mutation was performed.

| Observation | infra | linux |
| --- | --- | --- |
| SSH reachable | Yes | Yes |
| OS / architecture | Linux 6.12.0-211.56.1.el10_2.0.1.x86_64 / x86-64 | Same |
| CPUs | 16 | 12 |
| Available memory at observation | 57,006,072 kB | 13,716,644 kB |
| Free space on /var filesystem | 18.0 GiB | 14.7 GiB |
| podman, systemctl, bwrap | Present | Present |
| Go, Node, Codex commands | Present | Present |
| Codex version output | codex-cli 0.153.4 | codex-cli 0.153.4 |
| Typst command | Absent from command path | Absent from command path |
| max_user_namespaces | 253563 | 190272 |

Inventory used uname, Python standard-library reads of memory/disk/tool paths and namespace limit, and codex --version. Available-memory/disk values are transient observations. Existing personal Codex state was not inspected or used. Tool presence and a nonzero namespace limit are not proof that an isolated runner works; actual positive/negative containment and supervisor-death tests remain I12 requirements.

An owner question about preferred app/runner placement is pending. Both machines are candidates with the existing protocol version; no placement has been selected. Deployment must use dedicated private runtime state and an isolated execution boundary, not the user's existing Codex account/configuration. Typst still needs a deployable installation for the app/pack service. Private endpoint, app-to-runner transport, bridge secret, host artifact pin and supported owner sign-in remain unconfigured for this product.
