FROM golang:1.25-alpine AS build

WORKDIR /src
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gin-bear ./cmd

FROM alpine:3.22

RUN addgroup -S app && adduser -S app -G app
WORKDIR /app

COPY --from=build /out/gin-bear /app/gin-bear
COPY application.yaml /app/application.yaml
COPY locales /app/locales

USER app
EXPOSE 8080

ENTRYPOINT ["/app/gin-bear"]
