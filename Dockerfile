FROM golang:1.17

COPY . /src
WORKDIR /src

RUN go build && go build ./cmd/bulk_ingest
ENTRYPOINT ["./bulk_ingest"]
