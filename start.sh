#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "Usage: $0 WECHAT_APPID WECHAT_APPSECRET WECHAT_TOKEN WECHAT_AESKEY"
  exit 1
fi

export WECHAT_APPID="$1"
export WECHAT_APPSECRET="$2"
export WECHAT_TOKEN="$3"
export WECHAT_AESKEY="$4"

exec go run .