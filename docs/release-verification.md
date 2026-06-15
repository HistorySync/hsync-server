# Release Artifact Verification

This repository writes a machine-readable artifact manifest during release
preparation so operators can verify that a binary or container image matches a
specific source commit without relying on an external supply-chain platform.

## Generate release artifacts

From the CE repository root:

```powershell
make vuln-check
make sbom
```

The main outputs are:

- `build/release-artifact-manifest-ce.json`
- `build/release-report-ce.json`
- `build/sbom/go-modules-ce.cdx.json`
- `build/sbom/image-ce.cdx.json`

`build/release-artifact-manifest-ce.json` records:

- `commit`
- `build_info`
- `binary.sha256`
- `image.digest`
- `sbom.go_modules.path`
- `sbom.docker_image.path`

## Verify a CE binary against a commit

1. Compare the binary hash with the manifest:

```powershell
Get-FileHash .\build\artifacts\hsync-server.exe -Algorithm SHA256
```

The printed SHA-256 must match `binary.sha256` in
`build/release-artifact-manifest-ce.json`.

2. Read the embedded build metadata directly from the binary:

```powershell
.\build\artifacts\hsync-server.exe version --format json
```

Confirm that `build_info.commit` matches the commit you expect and that the
other fields line up with the manifest's `build_info`.

## Verify a CE image against a commit

1. Compare the local image digest with the manifest:

```powershell
docker image inspect historysync/server:release-<short-commit> --format '{{.Id}}'
```

That digest must match `image.digest` in the artifact manifest.

2. Check the OCI revision label:

```powershell
docker image inspect historysync/server:release-<short-commit> --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'
```

The revision label must match the manifest `commit`.

3. If the image has been pushed to a registry, compare any populated
`RepoDigests` output with `image.repo_digests`.
