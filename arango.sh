#!/bin/sh
# https://www.arangodb.com/docs/stable/tutorials-reduce-memory-footprint.html
exec docker run -d --restart always -p 8529:8529 -e ARANGO_ROOT_PASSWORD=password -v $(pwd)/data:/var/lib/arangodb3 arangodb/arangodb:3.9.1
