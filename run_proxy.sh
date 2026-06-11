#!/bin/bash

set -e

direnv exec . go run cmd/codex-oauth-proxy/main.go
