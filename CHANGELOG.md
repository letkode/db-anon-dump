# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). Version tags have no `v` prefix (`0.1.0`).

## [Unreleased]

## [0.2.0] - 2026-11-19

### Added
- The source password is now read automatically from `.env` files next to `config.yaml`
  (`DBANONDUMP_SOURCE_PASSWORD`, or the name set in `source.password_env`). Files are merged in this order,
  the last one winning: `.env`, `.env.local`, `.env.<APP_ENV>`, `.env.<APP_ENV>.local`. `APP_ENV` comes from
  the environment or from `.env` / `.env.local`; without it only the first two are read. A variable already
  set in the real environment always wins over the files, and missing files are skipped.
- `dbanondump init` also writes `.env` (mode 0600) from the new `templates/.env.dist`.
- `.env`, `.env.*` (except `.env.dist`) added to `.gitignore`.
- New dependency: `github.com/joho/godotenv` (parses the `.env` files).
- Run log: `run` and `check` write `logs/<command>-<datetime>.log` (mode 0600) with every console line
  timestamped, the error and exit code on failure, and a summary with the total duration and the time of each
  phase (temporary database, structure, pre-flight, data extraction, anonymise, dump, cleanup). The source
  password is redacted. Configure it with `logs.dir` (`""` disables) and `logs.keep` in `config.yaml`, or with
  `--log-dir` / `--no-log`. `logs/` added to `.gitignore`.

### Changed
- `init` now writes four files (`config.yaml`, `columns.yaml`, `functions.yaml`, `.env`) and refuses to run
  if any of them already exists (unless `--force`).
- The "missing password" error now also mentions the `.env` path that was looked up.

## [0.1.0] - 2026-11-18

First release.

### Added
- `dbanondump run`: extracts the selected tables into a throwaway database (temporary Docker container),
  anonymises them with the patterns of `columns.yaml`, runs the post-anonymisation checks and writes the dev
  dump (`.sql` or `.sql.gz`). The temporary container is removed at the end, even on errors.
- Engines: MySQL, MariaDB and PostgreSQL (one engine per project; patterns are written in that engine's SQL).
- `dbanondump check`: validates against the real structure and lists personal-looking columns without a
  pattern (`--strict` exits 3 if any); loads no data and writes nothing.
- `dbanondump validate`: offline lint of the three configuration files, with no Docker or database.
- `dbanondump init [--force] [dir]`: writes commented starter `config.yaml`, `columns.yaml` and
  `functions.yaml` (templates embedded in the binary).
- Source can be a live database (read-only account) or an existing dump (`source.dump_file` / `--from-dump`).
- Table selection with names, globs and excludes (`--tables`, `--exclude`).
- Source password taken from an environment variable (`DBANONDUMP_SOURCE_PASSWORD`).
- Static binaries for linux/amd64, linux/arm64 and darwin/arm64 with `SHA256SUMS`, published by the
  `Release` workflow when a version tag is pushed.
- CI: `go vet`, unit tests and end-to-end tests for MySQL, MariaDB and PostgreSQL.

[Unreleased]: https://github.com/letkode/db-anon-dump/compare/0.2.0...HEAD
[0.2.0]: https://github.com/letkode/db-anon-dump/compare/0.1.0...0.2.0
[0.1.0]: https://github.com/letkode/db-anon-dump/releases/tag/0.1.0
