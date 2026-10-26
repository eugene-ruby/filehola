FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /filehola ./cmd/filehola

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 filehola
USER filehola
COPY --from=build /filehola /usr/local/bin/filehola
COPY LICENSE /LICENSE
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/filehola"]
