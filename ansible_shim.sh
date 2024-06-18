#!/usr/bin/env sh
set -e

ACCESS_TOKEN="`curl $OPAAL_URL/token | jq -r '.access_token'`"
export ACCESS_TOKEN
exec ansible-playbook main.yaml
