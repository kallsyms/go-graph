#!/bin/bash

cat responses/* | jq -r '.[].nameWithOwner' | \
    awk '{print "https://github.com/" $1 ".git"}'
