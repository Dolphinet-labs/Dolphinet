FROM --platform=$BUILDPLATFORM golang:1.24-alpine3.21 AS builder

ARG VERSION=v0.0.0

RUN apk add --no-cache make gcc musl-dev linux-headers git jq bash

# build op-node with the shared go.mod & go.sum files
COPY ./dn-node /app/dn-node
COPY ./dn-service /app/dn-service
COPY ./common /app/common
COPY ./dn-supervisor /app/dn-supervisor
COPY ./dn-deployer /app/dn-deployer

COPY ./go.mod /app/go.mod
COPY ./go.sum /app/go.sum

WORKDIR /app/dn-node

RUN go mod tidy

ARG TARGETOS TARGETARCH

RUN go build -o bin/dn-node ./cmd/main.go

FROM alpine:3.21

COPY --from=builder /app/dn-node/bin/dn-node /usr/local/bin

CMD ["dn-node"]
