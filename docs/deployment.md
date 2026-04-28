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
- config: `/etc/filegate/conf.yaml`
- systemd unit: `/lib/systemd/system/filegate.service`
- data/log dirs: `/var/lib/filegate`, `/var/log/filegate`

Service is installed but not auto-started by design.

## systemd Operations

```bash
sudo systemctl daemon-reload
sudo systemctl enable filegate
sudo systemctl start filegate
sudo systemctl status filegate
```

## Container Deployment

Use the provided Dockerfile or compose examples.

For production, mount:

- file roots
- index path
- persistent logs (optional)

and inject token/config through env vars or mounted config file.
