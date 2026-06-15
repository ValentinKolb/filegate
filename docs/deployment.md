# Deployment

## Build Targets

- static Linux binaries via GoReleaser
- `.deb` and `.rpm` via nfpm
- systemd service unit included in packages

Source files:

- GoReleaser config: [`/.goreleaser.yaml`](https://github.com/ValentinKolb/filegate/blob/main/.goreleaser.yaml)
- systemd unit: [`packaging/systemd/filegate.service`](https://github.com/ValentinKolb/filegate/blob/main/packaging/systemd/filegate.service)
- default config: [`packaging/config/conf.yaml`](https://github.com/ValentinKolb/filegate/blob/main/packaging/config/conf.yaml)

## Local Release Build

```bash
goreleaser release --snapshot --clean
```

Artifacts are written to `dist/`.

## Package Install

This is the recommended production deployment path: install the package, configure `/etc/filegate/conf.yaml`, and run Filegate through `filegate.service`.

### Debian/Ubuntu

```bash
sudo dpkg -i ./dist/filegate_<version>_linux_amd64.deb
```

### Rocky/RHEL

```bash
sudo rpm -Uvh ./dist/filegate-<version>-1.x86_64.rpm
```

The package installs:

- binary: `/usr/bin/filegate`
- short command link: `/usr/bin/fg`
- config: `/etc/filegate/conf.yaml`
- systemd unit: `/lib/systemd/system/filegate.service`
- data/log dirs: `/var/lib/filegate`, `/var/log/filegate`

Service is installed but not auto-started by design.

The `fg` command works after package install. To also install the optional shell alias at package install time:

```bash
sudo FILEGATE_INSTALL_ALIAS_FG=1 dpkg -i ./dist/filegate_<version>_linux_amd64.deb
# or:
sudo FILEGATE_INSTALL_ALIAS_FG=1 rpm -Uvh ./dist/filegate-<version>-1.x86_64.rpm
```

The postinstall script writes `alias fg='filegate'` to the invoking user's `.bashrc` or `.zshrc`. For other shells it prints the snippet and leaves shell config unchanged.

## Package Upgrade

Package upgrades are offline operations. The preinstall script refuses to replace package files while `filegate.service` is active, so stop the daemon before upgrading:

```bash
sudo systemctl stop filegate
sudo dpkg -i ./dist/filegate_<version>_linux_amd64.deb
# or:
sudo rpm -Uvh ./dist/filegate-<version>-1.x86_64.rpm
sudo systemctl start filegate
sudo systemctl status filegate
```

If the service is still running, the package manager exits before installing the new version and prints the stop instruction. The script does not stop the service automatically.

## systemd Operations

```bash
sudo systemctl daemon-reload
sudo systemctl enable filegate
sudo systemctl start filegate
sudo systemctl status filegate
```

## Container Deployment

Use the provided Dockerfile or compose examples for local evaluation, CI smoke tests, or environments that explicitly standardize on containers. For ordinary Linux production hosts, prefer package install plus systemd.

For production, mount:

- file roots
- index path
- persistent logs (optional)

and inject token/config through env vars or mounted config file.

## Admin App

The admin UI is shipped as a standalone SSR app in `admin/`, not as part of the
Filegate REST binary. Run it next to Filegate and give it:

- `FILEGATE_URL` — the REST API URL the admin server can reach
- `FILEGATE_TOKEN` — the Filegate bearer token kept server-side
- `ADMIN_TOKEN` — optional separate login token for browser users

Uploads use Filegate upload sessions with direct session tokens, so large file
and folder uploads go browser-to-Filegate after the admin app creates the
sessions. Downloads use scoped direct download URLs. If the browser reaches
Filegate at a different URL than the admin server, set `server.public_url` in
Filegate and configure CORS for the admin origin.
