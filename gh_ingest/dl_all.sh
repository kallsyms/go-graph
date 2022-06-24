#!/bin/bash

cat responses/* | jq -r '.[] | select(.stargazers.totalCount >= 100) | .nameWithOwner' | shuf | \
    xargs -n 1 -P 4 -I '{}' bash -c 'GIT_TERMINAL_PROMPT=0 git clone --depth 1 --no-single-branch https://github.com/{} 100star_repos/{}'
    #xargs -n 1 -P 4 -I '{}' bash -c 'if [[ ! -d "repos/{}" && ! -d "/mnt/hdd2/gh_ingest2/repos/{}" ]]; then GIT_TERMINAL_PROMPT=0 git clone --depth 1 --no-single-branch https://github.com/{} repos/{}; fi'
