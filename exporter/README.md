# Upgrade-readiness-exporter

De Go-code hoort in deze map. De build, de uitrol, de rechten en het scrapen staan al klaar in `base/exporter-*.yaml`.

## Wat de code moet doen

- Zet `go.mod` en het `main`-package direct in deze map. De `Dockerfile` draait `go build .` vanuit hier.
- Serveer Prometheus-metrics op poort `8080`, pad `/metrics`, via gewone HTTP.
- Gebruik voor de Kubernetes-API de in-cluster-config. Het ServiceAccount `upgrade-readiness-exporter` mag alleen lezen.
- Schrijf naar geen enkel bestand: het root-filesystem is read-only. Heb je toch schrijfruimte nodig, voeg dan een `emptyDir` toe aan de Deployment.

Heeft de exporter een extra resource nodig? Voeg die dan toe aan de `ClusterRole` in `base/exporter-rbac.yaml`.

## Aanzetten

Voer deze stappen op volgorde uit:

1. Merge de Go-code naar `main`. De `BuildConfig` bouwt vanaf die branch.
2. Bouw het eerste image:

   ```bash
   oc start-build upgrade-readiness-exporter -n clusterbusters --follow
   ```

3. Zet `replicas` in `base/exporter-deployment.yaml` op `1` en merge dat naar `main`. Argo CD rolt de pod daarna uit.

Doe stap 3 niet vóór stap 2. Zonder image komt de pod in `ImagePullBackOff` en wordt de Argo CD-app `Degraded`.

## Nieuwe versie uitrollen

Na een merge van nieuwe code bouw je opnieuw en herstart je de pod. Er is geen automatische trigger.

```bash
oc start-build upgrade-readiness-exporter -n clusterbusters --follow
```

```bash
oc rollout restart deployment/upgrade-readiness-exporter -n clusterbusters
```

## Controleren

User workload monitoring scrapet de exporter elke 60 seconden. Het label `namespace="clusterbusters"` onderscheidt de metrics van die van het platform.

```promql
up{namespace="clusterbusters", service="upgrade-readiness-exporter"}
```

## Dashboard

`base/perses-dashboard.yaml` bevat nog geen panelen voor de metrics van de exporter, omdat de metricnamen nog niet vastliggen. Voeg die panelen toe zodra de namen bekend zijn.
