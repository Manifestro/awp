# Releasing and Hosting the AWP Installer

## Release a version

The release workflow runs for tags beginning with `v`:

```bash
git tag v0.1.0-alpha.1
git push origin v0.1.0-alpha.1
```

GitHub Actions builds these archives:

```text
awp_0.1.0-alpha.1_darwin_amd64.tar.gz
awp_0.1.0-alpha.1_darwin_arm64.tar.gz
awp_0.1.0-alpha.1_linux_amd64.tar.gz
awp_0.1.0-alpha.1_linux_arm64.tar.gz
awp_0.1.0-alpha.1_checksums.txt
```

The installer resolves `/releases/latest`, downloads the matching archive, and verifies it against the checksum file.

## Host `awp.manifestro.io`

The `site` directory is deployed by `.github/workflows/pages.yml`. It contains the public `install.sh` and the custom-domain `CNAME` file.

Repository setup:

1. In GitHub repository settings, set **Pages → Source** to **GitHub Actions**.
2. In the DNS zone for `manifestro.io`, create:

   ```text
   Type:  CNAME
   Name:  awp
   Value: manifestro.github.io
   ```

3. Wait for GitHub Pages to verify the custom domain and issue TLS.
4. Enable **Enforce HTTPS**.
5. Verify the exact public file:

   ```bash
   curl -LsSf https://awp.manifestro.io/install.sh | sh
   ```

No release can be installed until at least one `v*` tag has completed the Release workflow successfully.

## Installer environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `AWP_VERSION` | `latest` | Release tag or version, with or without the leading `v`. |
| `AWP_INSTALL_DIR` | `$HOME/.local/bin` | Destination directory. |
| `AWP_REPOSITORY` | `Manifestro/awp` | Alternate GitHub repository for testing forks. |
