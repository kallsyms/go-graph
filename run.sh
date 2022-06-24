#!/bin/bash

set -ueo pipefail

docker build -t go-graph-ingest .
docker run --rm -v /mnt/fio/gh_ingest/100star_repos/:/repos go-graph-ingest -clean -db http://root:password@192.168.1.210:8529 -repos /repos 2>&1 | tee log
