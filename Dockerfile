# ---- Stage 1: build ----
FROM golang:1.26 AS build
WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change
COPY go.mod go.sum ./
RUN go mod download

# Then the source
COPY cmd/ cmd/

ENV CGO_ENABLED=0

RUN go build -o /out/server ./cmd/server &&\
    go build -o /out/client ./cmd/client

# ---- Stage 2: run ----
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/server /out/client /usr/local/bin/
USER nonroot
