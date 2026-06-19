FROM golang:1.25-alpine AS build

WORKDIR /src
RUN apk add --no-cache git ca-certificates

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/duiniwukenaihe/gin-bear/pkg/bear.Version=${VERSION} -X github.com/duiniwukenaihe/gin-bear/pkg/bear.Commit=${COMMIT} -X github.com/duiniwukenaihe/gin-bear/pkg/bear.BuildTime=${BUILD_TIME}" -o /out/gin-bear ./cmd

FROM alpine:3.22

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

LABEL org.opencontainers.image.title="gin-bear" \
      org.opencontainers.image.description="Production-ready Gin-Bear application image" \
      org.opencontainers.image.source="https://github.com/duiniwukenaihe/gin-bear" \
      org.opencontainers.image.version=${VERSION} \
      org.opencontainers.image.revision=${COMMIT} \
      org.opencontainers.image.created=${BUILD_TIME}

RUN addgroup -S app && adduser -S app -G app
WORKDIR /app

COPY --from=build /out/gin-bear /app/gin-bear
COPY application.yaml /app/application.yaml
COPY locales /app/locales

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:8080/live || exit 1

ENTRYPOINT ["/app/gin-bear"]
