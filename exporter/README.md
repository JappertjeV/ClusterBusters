# Upgrade-readiness-exporter

Go-app die het cluster scant op dingen die een upgrade laten mislukken, en de bevindingen als
Prometheus-metrics op `:8080/metrics` zet. De build, de uitrol, de rechten, het scrapen en de
alerts staan in `base/exporter-*.yaml`.

| Bestand | Inhoud |
|---|---|
| `main.go` | config, HTTP-server (`/metrics`, `/healthz`), scan-loop |
| `scan.go` | lijst van checks, score en ready |
| `thanos.go` | queries naar Thanos Querier |
| `checks.go` | de checks zelf, met de titel- en fix-teksten in het Engels en Nederlands |
| `collector.go` | zet het laatste scanresultaat om in metrics |
| `version.go` | doelversie bepalen, Kubernetes- naar OpenShift-versie |
| `web.go`, `web/index.html` | de KPN-dashboardpagina op `/` en de live data op `/api/status` |
| `platform.go` | ruwe platform-metrics uit Thanos voor de pagina Platform |

## Checks

| check | Leest | Blocker | Warning |
|---|---|---|---|
| `clusterversion` | ClusterVersion, admin-gates/admin-acks | Failing, Upgradeable=False | Progressing, onbevestigde admin-gate (info: bekende update-risico's, `RetrievedUpdates=False`) |
| `operators` | ClusterOperators | niet Available, Degraded, Upgradeable=False | Progressing |
| `olm` | ClusterServiceVersions, Subscriptions, InstallPlans | phase ≠ Succeeded, `olm.maxOpenShiftVersion` < doel, `InstallPlanFailed` | Subscription `ResolutionFailed` of `CatalogSourcesUnhealthy`, InstallPlan wacht op goedkeuring |
| `nodes` | Nodes, MachineConfigPools | Node niet Ready, MCP Degraded | Pressure, cordoned, MCP paused of niet Updated |
| `pdb` | PodDisruptionBudgets | 0 disruptions allowed | PDB matcht geen pods |
| `deprecated_apis` | APIRequestCounts, `apiserver_requested_deprecated_apis` via Thanos | gebruikte API verdwijnt in of vóór de doelversie | gebruikte API verdwijnt later (geen verwijderversie bekend: info) |
| `unsupported` | `operator.openshift.io` (`cluster`), Pods | `unsupportedConfigOverrides` gezet | losse pod zonder controller |

`deprecated_apis` gebruikt twee bronnen, omdat `APIRequestCount` leeg kan zijn, zoals op `crc03`. Staat een API in beide, dan telt die van `APIRequestCount`, want alleen die noemt de gebruikers. De metric blijft op 1 staan tot de API-server herstart, dus een opgeloste API verdwijnt pas daarna uit de bevindingen. Is Thanos onbereikbaar of heeft Prometheus geen data van de API-server, dan faalt de check.

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
| `upgrade_readiness_version_info` | `current_version`, `target_version` | altijd 1 |

Elke scan vervangt alle bevindingen, dus wat opgelost is verdwijnt bij de volgende scan.

## Instellingen

| Env | Standaard | |
|---|---|---|
| `SCAN_INTERVAL` | `60` | seconden tussen scans |
| `TARGET_VERSION` | leeg | doelversie, bv. `4.19`; leeg = de minor na de huidige, dus het doel schuift mee met elke upgrade. Staat in `base/exporter-deployment.yaml` |
| `INCLUDE_PLATFORM` | `false` | ook platform-namespaces meenemen |
| `ACCEPTED_FINDINGS` | leeg | bekende punten die niet meetellen: reguliere expressies, gescheiden door `;`, gezocht in `<check>/<resource>: <title>`. Een treffer wordt info met "Accepted" ervoor. De Deployment zet de twee CRC-punten (overrides in de ClusterVersion, DeploymentConfig-API) |
| `LISTEN_ADDR` | `:8080` | |
| `THANOS_URL` | `https://thanos-querier.openshift-monitoring.svc:9091` | voor `deprecated_apis`; buiten het cluster faalt die check, tenzij je een bereikbaar adres opgeeft |

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

De Deployment staat op 1 replica. Op een nieuw cluster bestaat het image nog niet, dus staat de pod
op `ImagePullBackOff` tot je het eerste image bouwt:

```bash
oc start-build upgrade-readiness-exporter -n clusterbusters --follow
```

```bash
oc rollout restart deployment/upgrade-readiness-exporter -n clusterbusters
```

Het volledige stappenplan staat in `docs/cluster-opzetten.md`.

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

`base/perses-dashboard.yaml` toont alle metrics hierboven in de secties "Overview", "What needs fixing" en "Score and checks". De tabel "What needs fixing" komt uit `upgrade_readiness_finding == 1`.

De labels `title` en `fix` zijn Engels. De KPN-pagina is standaard Engels en haalt voor de NL-knop de Nederlandse tekst uit `/api/status`.

## Demo

Zie `testcases/README.md`.

## KPN-dashboardpagina

Naast het Perses-dashboard serveert de exporter een eigen pagina in KPN-huisstijl. Perses kan
het lettertype, de animaties en losse pagina's niet aan; deze pagina wel. De gegevens komen van
dezelfde scan als de metrics.

- `/`: de pagina (`web/index.html`, ingebouwd in de binary; lettertype en logo zitten in het bestand)
- `/api/status`: de laatste scan als JSON. De pagina ververst elke 15 seconden.
- `/api/resilience`: opgenomen foutruns uit `web/resilience-runs.json`, voor de pagina Resilience. Vaste data van een lokaal kind-cluster, niet live en niet van dit cluster. De pagina kan geen fouten starten: de Route heeft geen login.

Openen via de Route:

```bash
oc get route upgrade-readiness -n clusterbusters
```

Of zonder Route, via je eigen computer op http://localhost:8080:

```bash
oc port-forward -n clusterbusters deploy/upgrade-readiness-exporter 8080:8080
```

De pagina heeft geen login. De scoregrafiek onthoudt de laatste 180 scans in het geheugen en
begint na een herstart van de pod opnieuw.
