#!/bin/bash
# Sync feature files from angzarr core repo
# Assumes angzarr core is cloned at ../angzarr or ANGZARR_CORE_PATH is set

CORE=${ANGZARR_CORE_PATH:-"../angzarr"}

if [ ! -d "$CORE/features/client" ]; then
    echo "Error: Cannot find angzarr core at $CORE"
    echo "Clone it or set ANGZARR_CORE_PATH"
    exit 1
fi

mkdir -p features
cp -r "$CORE/features/client/"*.feature features/ 2>/dev/null || true
echo "Features synced from $CORE"
