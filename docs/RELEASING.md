# Releasing and Hosting the AWP Installer

## Release a version

The release workflow runs for tags beginning with `v`:

```bash
git tag v0.2.0-alpha.1
git push origin v0.2.0-alpha.1
```

GitHub Actions builds these archives:

```text
awp_0.2.0-alpha.1_darwin_amd64.tar.gz
awp_0.2.0-alpha.1_darwin_arm64.tar.gz
awp_0.2.0-alpha.1_linux_amd64.tar.gz
awp_0.2.0-alpha.1_linux_arm64.tar.gz
awp_0.2.0-alpha.1_checksums.txt
```

The installer resolves `/releases/latest`, downloads the matching archive, and verifies it against the checksum file.

## Host `awp.manifestro.io`

The installer is served by the Manifestro Next.js website, not GitHub Pages. Its canonical source is [`site/install.sh`](../site/install.sh) in this repository.

Copy it to `public/install.sh` in the Next.js application during development or deployment. Next.js then exposes the static asset at `/install.sh`. Configure `awp.manifestro.io` to point to that application and verify:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | sh
```

The website only serves the installer script. The script detects the platform and downloads and verifies the matching binary from GitHub Releases.

No release can be installed until at least one `v*` tag has completed the Release workflow successfully.

## Installer environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `AWP_VERSION` | `latest` | Release tag or version, with or without the leading `v`. |
| `AWP_INSTALL_DIR` | `$HOME/.local/bin` | Destination directory. |
| `AWP_REPOSITORY` | `Manifestro/awp` | Alternate GitHub repository for testing forks. |
