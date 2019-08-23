#!/usr/bin/env bash

set -exu
GOOS="$1" go build -o "cidr_$1" cidr.go
