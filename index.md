# LocalRouter Project Index

## Overview
LocalRouter is an OpenAI-compatible endpoint that routes LLM requests to the best available provider based on priority, health, and rate limits.

## Table of Contents
- [Root Files](#root-files)
- [Directories](#directories)
- [Internal Packages](#internal-packages)
- [Scripts](#scripts)
- [Templates and Tests](#templates-and-tests)

## Root Files

| File | Description |
|------|-------------|
| `.gitignore` | Specifies files and directories to be ignored by Git, including binaries, logs, IDE folders, and build artifacts. |
| `.goreleaser.yaml` | Configuration file for GoReleaser, defining build targets, archive formats, checksum generation, and changelog organization for multi-platform releases. |
| `config.yaml` | Main configuration file defining routing rules, provider endpoints, API keys, rate limits, and model priorities for the LocalRouter service. |
| `Dockerfile` | Multi-stage Docker build configuration that creates a static Go binary and packages it in an Alpine-based container with port 8080 exposed. |
| `go.mod` | Go module definition file specifying the module path, Go version (1.22), and project dependencies including chi router, fsnotify, and YAML parser. |
| `go.sum` | Go checksum file containing cryptographic hashes of all direct and indirect dependencies to ensure reproducible builds. |
| `LICENSE` | MIT License file granting permissions for use, modification, and distribution of the software with standard copyright and liability disclaimers. |
| `README.md` | Comprehensive project documentation including setup instructions, configuration reference, API documentation, and project structure overview. |
| `report.md` | Generated report file containing provider status information, error analysis, and system metrics. |

## Directories

| Directory | Description |
|-----------|-------------|
| `cmd/` | Contains the main localrouter command-line application entry point. |
| `docs/` | Contains project documentation including a project.md file and a superpowers subdirectory. |
| `internal/` | Contains internal implementation modules for cache, config, discovery, health, limits, metrics, provider, registry, reqid, router, server, startup, and state functionality. |
| `scripts/` | Contains automation scripts for installation, release, running, and validation (PowerShell and shell scripts). |
| `templates/` | Contains template files including a report.html template. |
| `test/` | Contains end-to-end (e2e) test suite. |

## Internal Packages

| Package | Description |
|---------|-------------|
| `internal/cache/` | Manages cached files and resources for performance optimization. |
| `internal/config/` | Contains configuration files for system settings and defaults. |
| `internal/discovery/` | Handles dynamic discovery and metadata retrieval of application components. |
| `internal/health/` | Provides health checks and status information for the router services. |
| `internal/limits/` | Enforces capacity and usage limits to prevent abuse. |
| `internal/metrics/` | Collects and processes application performance and usage metrics. |
| `internal/provider/` | Interfaces with external providers and services for dependency management. |
| `internal/registry/` | Manages repositories and package registries for components. |
| `internal/reqid/` | Registers reference IDs for component tracking and dependencies. |
| `internal/router/` | Core management of router configuration, routing rules, and state. |
| `internal/server/` | Defines server-related logic, services, and endpoints. |
| `internal/startup/` | Handles startup processes and initialization sequences. |
| `internal/state/` | Manages application state and persistence mechanisms. |

## Scripts

| Script | Description |
|--------|-------------|
| `scripts/install.ps1` | Windows PowerShell script that installs LocalRouter as a Windows Service using NSSM, with actions for install/uninstall/start/stop/status, builds the binary, sets up directory structure, configures service parameters, and manages API key environment variables. |
| `scripts/release.ps1` | PowerShell script that validates version format, checks for uncommitted changes and master branch, runs tests, creates annotated git tag, and pushes it to trigger GitHub Actions release workflow. |
| `scripts/release.sh` | Bash equivalent of release.ps1 that performs the same release validation, testing, tagging, and pushing operations for non-Windows systems. |
| `scripts/run.ps1` | PowerShell script that builds the LocalRouter binary and runs it with a specified config file, with commented examples of environment variables for various LLM providers. |
| `scripts/run.sh` | Bash equivalent of run.ps1 that builds and runs the LocalRouter binary with configurable config file for Unix-like systems. |
| `scripts/validator.ps1` | PowerShell test script that validates LocalRouter functionality by testing health endpoints, model availability, and routing behavior. |

## Templates and Tests

| Template/Test | Description |
|---------------|-------------|
| `templates/report.html` | HTML template for provider status reports with Bootstrap styling, status badges, and dynamic data sections for healthy, degraded, and unreachable providers. |
| `test/e2e/` | End-to-end testing directory with Playwright configuration, global setup, and test specifications. |