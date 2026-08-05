<p align="center">
<img with="290" height="233" src="https://s3.amazonaws.com/dsc-misc/img/acyl.png" alt="Acyl chloride" />
</p>

# Acyl

> [!WARNING]
> **Unmaintained. Archived for reference — do not deploy this.**
>
> Acyl is no longer developed, supported, or monitored for security issues. The last substantive commit was February 2022. It is pinned to Kubernetes 1.22 / Helm 3.7 / Go 1.16-era libraries with several years of unpatched CVEs, and by design it runs with cluster-admin privileges, holds a GitHub App private key, and accepts webhooks from the internet. That combination should not be run anywhere.
>
> This repository stays public as a portfolio artifact. Acyl ran as part of the Dollar Shave Club delivery pipeline from 2016 onward, and the design may still be interesting — in particular the transitive cross-repository environment composition, where one repo's `acyl.yml` pulls in other teams' in-progress branches to build a complete stack. The original `dollarshaveclub/*` repositories were deleted; this is the surviving copy.
>
> **For per-PR ephemeral Kubernetes environments today**, use [Argo CD ApplicationSets with the Pull Request generator](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators-Pull-Request/), [vCluster](https://www.vcluster.com/) for stronger isolation, or a hosted product such as Okteto, Signadot, or Bunnyshell.

*Testing Environments On Demand*


Acyl is a server and CLI utility for creating dynamic testing environments on demand in Kubernetes, triggered by GitHub Pull Requests.

Environments are defined by `acyl.yml` in your source code repository, and consist of one or more Helm Charts that are installed into a new Kubernetes namespace that is created on demand and torn down when you're finished. Environment lifecycles are tied to Pull Requests: a new environment is created when you open a PR, it is updated as you push additional commits to the PR (including force pushes), and finally it is destroyed when the PR is closed or merged, automatically.

Acyl includes features to make team collaboration and environment configuration easier for multiple teams working on complex application stacks, allowing teams to maintain separate isolated testing environments but share revisions of in-progress code when needed.

Acyl has been used in various forms as part of the core Dollar Shave Club software delivery pipeline since 2016, as described in a [2018 blog post](https://web.archive.org/web/20201109023625/https://engineering.dollarshaveclub.com/qa-environments-on-demand-with-kubernetes-5a571b4e273c) (the DSC engineering blog is gone; this is an Internet Archive snapshot).

## Web UI

Acyl includes a full web UI with authentication/authorization based upon [GitHub Apps](https://developer.github.com/apps/).

<p align="center">
  <img src="https://dsc-misc.s3.amazonaws.com/acyl-web-ui-event.png" width="300">
  <img src="https://dsc-misc.s3.amazonaws.com/acyl-web-ui-home.png" width="300">
  <img src="https://dsc-misc.s3.amazonaws.com/acyl-web-ui-env.png" width="300">
</p>

## Environment Configuration

Environments are defined by `acyl.yml`, which describes the required Helm Charts along with their release value configuration and the dependency relationships among them. The config file can be thought of as a "Helm compose", analagous to Docker Compose except using Helm Charts instead of individual containers. Acyl uses [Metahelm](https://github.com/bkeroack/metahelm) to construct a dependency graph of the environment charts and installs them in optimal reverse-dependency order.

An `acyl.yml` in one application repository can reference `acyl.yml` files in other repositories, and those applications (and their dependencies) will be transitively included in the environment. In this way complex application stacks maintained by different teams can share testing environment configuration.

### Examples

- Acyl is self-hosting: we use it to create testing environments for Acyl development itself. See [acyl.yml](https://github.com/bkeroack/acyl/blob/master/acyl.yml) in this repository.
- [Furan](https://github.com/bkeroack/furan) also uses Acyl for [testing environments](https://github.com/bkeroack/furan/blob/master/acyl.yml).

## Local Development

Acyl has CLI tools available to visualize, debug and test environment configurations locally.

`acyl config info` will validate, analyze and show a visualization for the acyl.yml in the current directory (which must be a valid git repository with GitHub remotes).

`acyl config test <create/update/delete>` will simulate PR open/push/close events and create, update or delete environments in a local Kubernetes cluster (Docker For Mac Kubernetes, MicroK8s, etc).

Run `acyl config --help` for the full set of local subcommands. (The Local Development wiki page that this section used to link to lived only in the deleted upstream repository and is gone.)

## Architecture

![Architecture](doc/acyl_architecture.png?raw=true)

- Acyl: This is the server application which listens for GitHub webhook events and performs operations to create, update or destroy environments within your Kubernetes cluster. The server is intended to run within the same cluster, with ClusterAdmin permissions. The Acyl binary also can be used as a local CLI utility for local environment development and debugging.
- Postgres: This is the primary datastore for Acyl.
- [Furan](https://github.com/bkeroack/furan): This is used to build and push application Docker images on demand.
- (*OPTIONAL*) Notifications can be sent to Slack channels or individual users when environments are created or altered.
- (*OPTIONAL*) [Vault](https://www.vaultproject.io/) can be used for Acyl secrets like GitHub tokens and database credentials.

## Building from source

Verified against Go 1.26 (August 2026): the vendored tree still compiles, and every test package that does not require a database still passes.

```sh
make build                                  # or: go build -mod=vendor -tags safe -o acyl .
go test -mod=vendor -tags safe ./pkg/...
```

The `safe` build tag is required for anything that touches the environment dependency graph. Acyl pins `gonum` to v0.9.1, whose graph iterators reach into Go runtime map internals via `//go:linkname`. Go 1.24 replaced Go's map implementation with Swiss tables, so that unsafe path now aborts with `fatal error: concurrent map iteration and map write`. The `safe` tag selects gonum's reflection-based iterator instead. Note that building *without* the tag still succeeds — the failure only appears at runtime.

The `pkg/persistence`, `pkg/api`, and `pkg/locker` test packages need a local PostgreSQL instance; see `.circleci/config.yml` for the expected environment. The vendored dependencies resolve reproducibly from `proxy.golang.org` even though the original `dollarshaveclub` repositories were deleted, so `go mod vendor` still reproduces the tree byte-for-byte.

## Further Reading
The User Guide and `acyl.yml` v2 specification lived in the upstream repository's wiki, which was lost when that repository was deleted. What remains in-tree:

- [acyl.yml v2 example, fully annotated](doc/acyl-v2.yml) — the practical substitute for the v2 specification
- [acyl.yml](acyl.yml) — the config Acyl used to build testing environments for itself
- [Status API](doc/status_api.md), [UI API](doc/ui_api.md), [UI branding](doc/ui_branding.md)
