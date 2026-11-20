# Contributing

Use Go 1.26 or later. The module selects the patched Go 1.27.4 toolchain;
the Docker image and CI use that version too. Keep `turborel` and
`charts` checkouts beside this repository when changing the database
integration or chart.

Read the [design](docs/design.md) before changing bootstrap, volume
identity, elections or recovery. Check database behavior against
Turborel's source and manual. A replica's SQL role is an observation,
not an instruction for the operator to promote it.

## Checks

```sh
make test test-integration security
make chart
make test-e2e
golangci-lint run
```

See [testing](docs/testing.md) for the end-to-end tools, fixtures and
limits. Reproduce a defect with a test at the level where it occurs.
Changes to the process manager, backup commands or replication need
the real-image suite; mocked clients cannot validate the native CLI.

The Go API is the source of generated deepcopy code and CRDs. Run
`make generate` and `make chart` after API changes. Commit the generated
CRDs in the charts repository with the matching chart change. There is
no Kustomize tree in this repository. Coordinate the chart and operator
versions before publishing either.

Update the README or manual when behavior changes. User-facing
Kubernetes documentation also belongs under `doc/ecosystem/` in the
Turborel repository. Examples must match the tested image and API.

## Container images

The Image workflow publishes `ghcr.io/turborel/turborel-operator:nightly`
and a tag for its source commit after both Test and Lint pass on main.
It builds Linux AMD64 and ARM64 images. A superseded main revision
cannot replace nightly, and pull requests never publish images.

In GitHub, require Test and Lint in branch protection and allow this
repository's Actions to write its package. The workflow uses
`GITHUB_TOKEN`; no registry password is needed. Set the container
package's visibility to public so the chart can pull it without
credentials. See [GitHub's container registry guide](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

## Commits

Follow Turborel's commit form: `area: imperative summary`, no full
stop, at most 75 characters. Explain the problem and resulting behavior
in the body, wrapped at 75 columns, and include relevant validation.
Keep one logical change per commit. Stage explicit paths and leave
build outputs, credentials and test clusters out of Git.

A change spanning repositories needs a commit in each; Git cannot make
those commits atomic across repositories. Name the corresponding
changes in the pull requests and arrange their release order.

The first pull request asks contributors to sign [CLA.md](CLA.md).
