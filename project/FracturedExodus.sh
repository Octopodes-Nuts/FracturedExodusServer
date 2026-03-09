#!/bin/sh
printf '\033c\033]0;%s\a' FracturedExodus
base_path="$(dirname "$(realpath "$0")")"
"$base_path/FracturedExodus.x86_64" "$@"
