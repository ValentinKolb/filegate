# Repository Structure

Top-level layout:

- `cmd/filegate`: main server/CLI binary
- `cmd/filegate-bench`: HTTP load benchmark tool
- `cli`: cobra/viper command implementation
- `adapter/http`: HTTP adapter layer
- `domain`: service logic and invariants
- `infra/*`: infrastructure modules (index, fs, detect, jobs, codec)
- `api/v1`: API request/response types
- `sdk/filegate`: Go SDK
- `packaging`: config, scripts, systemd unit
- `bench`: benchmark scripts and outputs
- `docs`: project documentation
- `skills`: agent skills for this repository
