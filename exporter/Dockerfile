# Statisch gebouwd (CGO uit), omdat ubi-micro geen C-bibliotheken of package manager heeft.
FROM registry.access.redhat.com/ubi9/go-toolset:latest AS build
WORKDIR /opt/app-root/src
COPY . .
RUN CGO_ENABLED=0 go build -o /tmp/exporter .

FROM registry.access.redhat.com/ubi9-micro:latest
COPY --from=build /tmp/exporter /usr/local/bin/exporter
USER 1001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/exporter"]
