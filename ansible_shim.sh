#!/usr/bin/env sh
set -e

ACCESS_TOKEN="`curl http://opaal:3333/token | jq -r '.access_token'`"
export ACCESS_TOKEN
exec ansible-playbook main.yaml
