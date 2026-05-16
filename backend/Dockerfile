FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN go mod tidy && CGO_ENABLED=0 go build \
    -ldflags "-s -w \
              -X marketplace/internal/buildinfo.Version=${VERSION} \
              -X marketplace/internal/buildinfo.Commit=${GIT_COMMIT} \
              -X marketplace/internal/buildinfo.Time=${BUILD_TIME}" \
    -o /out/server ./cmd/marketplace

FROM alpine:3.23
RUN apk add --no-cache git ca-certificates && \
    git config --system user.name "marketplace" && \
    git config --system user.email "marketplace@local"
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
ENTRYPOINT ["/app/server"]
