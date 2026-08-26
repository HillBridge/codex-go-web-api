FROM golang:1.25.3-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/user-order-api ./cmd/api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app

COPY --from=build /out/user-order-api /usr/local/bin/user-order-api

USER app
EXPOSE 8888

ENTRYPOINT ["/usr/local/bin/user-order-api"]
