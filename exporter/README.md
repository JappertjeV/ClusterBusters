# Upgrade-readiness-exporter

Go-app die het cluster scant op dingen die een upgrade laten mislukken, en de bevindingen als
Prometheus-metrics op `:8080/metrics` zet. De build, de uitrol, de rechten, het scrapen en de
alerts staan in `base/exporter-*.yaml`.

| Bestand | Inhoud |
|---|---|
| `main.go` | config, HTTP-server (`/metrics`, `/healthz`), scan-loop |
| `scan.go` | lijst van checks, score en ready |
| `checks.go` | de checks zelf, met de Nederlandse titel- en fix-teksten |
| `collector.go` | zet het laatste scanresultaat om in metrics |
| `version.go` | doelversie bepalen, Kubernetes- naar OpenShift-versie |

## Checks

| check | Leest | Blocker | Warning |
|---|---|---|---|
| `clusterversion` | ClusterVersion, admin-gates/admin-acks | Failing, Upgradeable=False | Progressing, onbevestigde admin-gate (bekende update-risico's: info) |
| `operators` | ClusterOperators | niet Available, Degraded, Upgradeable=False | Progressing |
| `olm` | ClusterServiceVersions | phase ≠ Succeeded, `olm.maxOpenShiftVersion` < doel | |
| `nodes` | Nodes, MachineConfigPools | Node niet Ready, MCP Degraded | Pressure, cordoned, MCP paused of niet Updated |
| `pdb` | PodDisruptionBudgets | 0 disruptions allowed | PDB matcht geen pods |
| `deprecated_apis` | APIRequestCounts | gebruikte API verdwijnt in of vóór de doelversie | gebruikte API verdwijnt later |
| `unsupported` | `operator.openshift.io` (`cluster`), Pods | `unsupportedConfigOverrides` gezet | losse pod zonder controller |

PDB's en pods in `openshift-*`, `openshift` en `kube-*` slaat hij over, tenzij `INCLUDE_PLATFORM=true`.
Een check die faalt (bijvoorbeeld door ontbrekende rechten) zet `upgrade_readiness_check_up{check}` op 0;
de andere checks lopen door.

## Metrics

| Metric | Labels | Waarde |
|---|---|---|
| `upgrade_readiness_score` | | 0–100: 100 − 25 per blocker − 5 per warning |
| `upgrade_readiness_ready` | | 1 = geen blockers en alle checks gelukt |
| `upgrade_readiness_findings` | `category`, `severity` | aantal, ook 0 |
| `upgrade_readiness_finding` | `check`, `category`, `severity`, `resource`, `title`, `fix` | altijd 1 |
| `upgrade_readiness_check_up` | `check` | 1 = gelukt, 0 = fout |
| `upgrade_readiness_last_scan_timestamp_seconds` | | unix-tijd laatste scan |

Elke scan vervangt alle bevindingen, dus wat opgelost is verdwijnt bij de volgende scan.

## Instellingen

| Env | Standaard | |
|---|---|---|
| `SCAN_INTERVAL` | `60` | seconden tussen scans |
| `TARGET_VERSION` | leeg | doelversie, bv. `4.19`; leeg = de minor na de huidige |
| `INCLUDE_PLATFORM` | `false` | ook platform-namespaces meenemen |
| `LISTEN_ADDR` | `:8080` | |

## Lokaal draaien en testen

Buiten het cluster gebruikt hij `~/.kube/config` van `oc login`, met jouw rechten.

```bash
cd exporter && go test ./...
```

```bash
cd exporter && go run .
```

```bash
curl -s localhost:8080/metrics | grep ^upgrade_readiness
```

Een extra resource nodig? Voeg die toe aan de `ClusterRole` in `base/exporter-rbac.yaml`. Het
root-filesystem in de pod is read-only.

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

`base/perses-dashboard.yaml` bevat nog geen panelen voor de metrics van de exporter. Voorstellen:

- Stat: `upgrade_readiness_score` en `sum(upgrade_readiness_findings{severity="blocker"})`
- Tabel "Wat moet er gefixt worden": `upgrade_readiness_finding == 1`, kolommen severity, resource, title, fix
- Time series: `upgrade_readiness_score`
