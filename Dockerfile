FROM golang:1.26.2 AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ribapuro
# Created here so that it can be copied in with the right owner below:
# the runtime image has no shell to mkdir with.
RUN mkdir -p /out/sites

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/ribapuro /app/ribapuro
COPY --from=build --chown=nonroot:nonroot /out/sites /app/sites
WORKDIR /app
USER nonroot
VOLUME /app/sites

EXPOSE 8080
ENTRYPOINT ["/app/ribapuro"]
