FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /out/server .

FROM alpine:3.20
RUN apk add --no-cache git ca-certificates && \
    git config --system user.name "marketplace" && \
    git config --system user.email "marketplace@local"
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
ENTRYPOINT ["/app/server"]
