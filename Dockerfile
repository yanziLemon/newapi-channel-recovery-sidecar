FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /sidecar .

FROM alpine:3.21
RUN adduser -D -H sidecar
COPY --from=build /sidecar /sidecar
USER sidecar
ENTRYPOINT ["/sidecar"]
