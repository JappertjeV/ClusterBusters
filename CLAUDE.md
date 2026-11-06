# ClusterBusters – OpenShift Platform Upgrade Readiness (KPN-hackathon)

Context voor Claude Code. Lees dit eerst, voordat je iets wijzigt.

## Opdracht
Bouw een geautomatiseerd readiness-platform voor OpenShift-upgrades. Het moet detecteren:
deprecated API's, ongezonde Operators, foute Pod Disruption Budgets, node-readiness-problemen
en unsupported resources. Het resultaat is een dashboard, plus alerting, dat duidelijk uitlegt
wat er gefixt moet worden voordat je upgradet.
Doel: minder mislukte upgrades door automatische platformvalidatie.

**Deadline: vrijdag 6 november 2026, 16:00.** Team van 3. Scope klein houden: liever werkend dan compleet.

### Eisen van de opdrachtgever
- De scanner in **Go**, niet in Python
- Het dashboard in **Perses**
- Uitrollen via **Argo CD** (OpenShift GitOps, staat al op het cluster)

## Architectuur
```
OpenShift API ──► Go-scanner (/metrics) ──scrape──► Prometheus ──► Thanos Querier ──► Perses-dashboard
                                                       └──► PrometheusRule / Alertmanager (alerting)
```
- Prometheus werkt met **pull**: de scanner zet alleen metrics klaar op `/metrics`. Een ServiceMonitor laat
  Prometheus ze ophalen. De scanner pusht nergens naartoe.
- Thanos Querier is een zoeklaag bovenop Prometheus. Perses stelt zijn queries daar.

## Omgeving
- OpenShift CRC, **extern gehost** door de opdrachtgever. Het team heeft **geen** `crc`-CLI, alleen `oc`.
- Single-node en niet echt te upgraden: we valideren de *readiness*, er wordt geen upgrade uitgevoerd.
- Geïnstalleerd: **Cluster Observability Operator 1.5.4** (CRD's `monitoring.rhobs/*`, `perses.dev/*`,
  `UIPlugin`) en **OpenShift GitOps** (namespace `openshift-gitops`).
- Namespace van het project: **`clusterbusters`**.

### Monitoring: route A gekozen
Op 5 nov geverifieerd: de platform-monitoring draait. `monitoringstack.yaml` en `thanosquerier.yaml` zijn
verwijderd; alles gaat via user workload monitoring (`monitoring.coreos.com/v1`). De tekst hieronder is
de afweging die daaraan voorafging.

Het plan was om `base/monitoringstack.yaml` en `base/thanosquerier.yaml` te verwijderen, omdat ze
dubbel zouden zijn met de ingebouwde platform-monitoring. **Dat klopt alleen als de platform-monitoring
inmiddels echt draait.** Eerder (5 nov, ±14:05) was `openshift-monitoring` leeg en bestond
`co/monitoring` niet: CRC zet de platform-monitoring standaard uit.

Controleer dit voordat je verwijdert:
```bash
oc get pods -n openshift-monitoring                    # verwacht: prometheus-k8s-0, thanos-querier-…
oc get co monitoring                                   # verwacht: Available=True
oc get pods -n openshift-user-workload-monitoring      # verwacht: prometheus-user-workload-0
```
- **Route A – draait het allemaal:** verwijder `monitoringstack.yaml` en `thanosquerier.yaml` uit `base/` en uit
  `base/kustomization.yaml`. Zet de datasource terug op
  `https://thanos-querier.openshift-monitoring.svc.cluster.local:9091`, met het TLS-blok en
  `secret: thanos-querier-secret` (zie git-historie, PR #7). User-workload-monitoring moet aanstaan
  (`cluster/cluster-monitoring-config.yaml`). De ServiceMonitor van de scanner wordt dan
  `monitoring.coreos.com/v1`, **niet** `monitoring.rhobs/v1`.
- **Route B – draait het niet:** laat de MonitoringStack en ThanosQuerier staan. Zonder die twee is er geen
  Prometheus en blijft het dashboard leeg. De ServiceMonitor wordt dan `monitoring.rhobs/v1`, met het label
  `app: upgrade-readiness`, zodat de MonitoringStack hem oppikt.

Kies één van de twee routes. Niet allebei, want dan scrapen twee Prometheussen hetzelfde en zie je dubbele data.

## Repo-structuur (JappertjeV/ClusterBusters, branch `main`)
```
argocd/application.yaml     Argo CD Application "clusterbusters" → path overlays/prod → ns clusterbusters
argocd/application-cluster.yaml  Application "clusterbusters-cluster" → path cluster (zonder prune)
argocd/rbac.yaml            recht van Argo CD op UIPlugins (eenmalig: oc apply -f argocd/)
base/                       Kustomize-base: monitoring, Perses-datasource, Perses-dashboard
overlays/prod/              namespace: clusterbusters + labels app: upgrade-readiness, env: productie
cluster/                    clusterbreed, via Argo CD: Cluster Observability Operator, UIPlugin, monitoring-config
bootstrap/                  GitOps Operator, met de hand: Argo CD kan zichzelf niet installeren
docs/cluster-opzetten.md    volgorde op een leeg cluster, en wat met de hand blijft
```
Regels:
- De namespace in de overlay **moet** gelijk zijn aan `spec.destination.namespace` van de Argo-app. Alleen daar
  heeft de controller rechten, via het label `argocd.argoproj.io/managed-by=openshift-gitops`.
- Argo heeft `prune: true` en `selfHeal: true`. Wat uit Git verdwijnt, verdwijnt ook uit het cluster. Handmatige
  wijzigingen in het cluster worden teruggedraaid. Wijzig dus via Git.
- Argo CD mag de `ClusterRole` en `ClusterRoleBinding` van de exporter wel uitrollen (`base/exporter-rbac.yaml`).
- De Application voor `cluster/` prunet bewust niet: een bestand weghalen mag de Operator niet verwijderen.
- Wijzig de naam van een `OperatorGroup` niet: een tweede in dezelfde namespace laat OLM falen.
- Kustomize-velden zijn hoofdlettergevoelig: `labels`, niet `Labels`.

## Metrics-contract (scanner → Prometheus → Perses)
| Metric | Labels | Waarde |
|---|---|---|
| `upgrade_readiness_score` | – | 0–100 (100 − 25 per blocker − 5 per warning, minimaal 0) |
| `upgrade_readiness_ready` | – | 1 = geen blockers en geen falende checks, anders 0 |
| `upgrade_readiness_findings` | `category`, `severity` | aantal bevindingen |
| `upgrade_readiness_finding` | `check`, `category`, `severity`, `resource`, `title`, `fix` | altijd 1 (info-metric: de tekst zit in de labels) |
| `upgrade_readiness_check_up` | `check` | 1 = check gelukt, 0 = fout (bv. geen rechten) |
| `upgrade_readiness_last_scan_timestamp_seconds` | – | unix-tijd van de laatste scan |
| `upgrade_readiness_version_info` | `current_version`, `target_version` | altijd 1 (info-metric: welke versie beoordeeld is) |

- `severity` is één van `blocker`, `warning` of `info`.
- **Roep `Reset()` aan op de GaugeVecs bij elke scan**, anders blijven opgeloste bevindingen in beeld.
- De huidige queries in `base/perses-dashboard.yaml` (`kube_poddisruptionbudget_*`, `cluster_operator_*`,
  `apiserver_requested_deprecated_apis`) zijn platform-metrics. Die werken alleen met route A. Zodra de
  scanner draait, moet het dashboard over op `upgrade_readiness_*`:
  - Stat: `upgrade_readiness_score`, `sum(upgrade_readiness_findings{severity="blocker"})`
  - Table "What needs fixing": `upgrade_readiness_finding == 1` (kolommen severity, resource, title, fix)
  - Time series: `upgrade_readiness_score` (in de demo zie je de score stijgen terwijl je fixt)

## Go-scanner (map `exporter/`, zie `exporter/README.md`)
- `client-go` **dynamic client** (`k8s.io/client-go/dynamic`) + `unstructured`, omdat de meeste resources
  OpenShift-CRD's zijn.
- Config: eerst `rest.InClusterConfig()`, en als dat faalt `clientcmd` (`~/.kube/config` van `oc login`).
  Zo werkt dezelfde binary lokaal en in het cluster.
- `prometheus/client_golang`: een HTTP-server op `:8080` met `/metrics` en `/healthz`, en een scan-loop
  elke `SCAN_INTERVAL` seconden (standaard 60).
- Env: `SCAN_INTERVAL`, `TARGET_VERSION` (leeg = volgende minor), `INCLUDE_PLATFORM` (standaard false).
- Eén falende check mag de scan niet stoppen. Zet dan `check_up{check}=0` en ga door met de rest.
- Een Python-referentie-implementatie bestaat (upgrade-readiness.zip, buiten de repo), met dezelfde checks
  en Nederlandse uitleg- en fix-teksten. Port de logica en teksten één-op-één.
- Taal: `title` en `fix` in de metrics zijn Engels, omdat Perses geen taalkeuze kent. De Nederlandse tekst
  staat alleen in `/api/status` (`titleNl`, `fixNl`) voor de EN/NL-knop op de KPN-pagina. Geef een nieuwe
  bevinding dus beide talen.

### Checks (GVR → wanneer blocker / warning)
| check | GVR | Logica |
|---|---|---|
| `clusterversion` | `config.openshift.io/v1 clusterversions` (naam `version`) | Failing=True → blocker; Upgradeable=False → blocker; Progressing=True → warning; `status.conditionalUpdates[].risks` → info; key in CM `openshift-config-managed/admin-gates` die niet `"true"` is in `openshift-config/admin-acks` → warning; `RetrievedUpdates=False` → info |
| `operators` | `config.openshift.io/v1 clusteroperators` | Available≠True, Degraded=True of Upgradeable=False → blocker; Progressing=True → warning |
| `olm` | `operators.coreos.com/v1alpha1 clusterserviceversions` | sla `status.reason=Copied` over; phase≠Succeeded → blocker; `olm.maxOpenShiftVersion` (in annotation `operatorframework.io/properties`) < doel-minor → blocker; ook `subscriptions` (ResolutionFailed/CatalogSourcesUnhealthy → warning, InstallPlanFailed → blocker) en `installplans` (phase RequiresApproval, niet approved → warning) |
| `nodes` | `v1 nodes`, `machineconfiguration.openshift.io/v1 machineconfigpools` | Node Ready≠True → blocker; Memory/Disk/PIDPressure of unschedulable → warning; MCP Degraded → blocker; MCP paused of Updated≠True → warning |
| `pdb` | `policy/v1 poddisruptionbudgets` | sla openshift-*/kube-* over (tenzij INCLUDE_PLATFORM); expectedPods=0 → warning; disruptionsAllowed=0 → blocker ("blokkeert node-drain, zet maxUnavailable: 1") |
| `deprecated_apis` | `apiserver.openshift.io/v1 apirequestcounts`, plus `apiserver_requested_deprecated_apis` via Thanos omdat APIRequestCount op CRC leeg is | `status.removedInRelease` gezet en `requestCount>0` → blocker als removed ≤ doel (OCP 4.N ≈ k8s 1.(N+13)), anders warning; toon de topgebruikers uit `status.last24h[].byNode[].byUser[]` |
| `unsupported` | `operator.openshift.io/v1` KubeAPIServer, Etcd, … (naam `cluster`); `v1 pods` | `spec.unsupportedConfigOverrides` gezet → blocker; losse pod zonder ownerReferences (Running) → warning |

### Deploy-onderdelen voor de scanner (in `base/`, behalve RBAC)
- Deployment + Service (poort `http`/8080, label `app: upgrade-readiness` via de overlay) + ServiceMonitor
  (`/metrics`, 30s). Gebruik de juiste apiGroup per route (zie het monitoring-blok hierboven).
- RBAC: alleen get/list/watch op de resources uit de checks-tabel, in een **ClusterRole** in
  `base/exporter-rbac.yaml`. Houd die gelijk met de checks in `exporter/checks.go`.
- Image: multi-stage Containerfile (UBI go-toolset → ubi-minimal, USER 1001). Bouwen via BuildConfig
  (binary/Docker-strategy) in het cluster, of een registry.
- Alerting: PrometheusRule (`UpgradeBlockersPresent: upgrade_readiness_ready == 0 for 2m`,
  `check_up == 0`, en een stale scan > 10 min). Gebruik de juiste apiGroup per route.

## Demo-scenario
1. Dashboard staat op (bijna) groen.
2. Maak bewust iets kapot (`testcases/demo-kapot.yaml`; uitgebreider: `testcases/testomgeving.yaml`): een Deployment met 1 replica + een PDB met `minAvailable: 1`, een losse pod en een PDB
   zonder matchende pods. Zet dit in een aparte `testcases/`-map. **Niet** in `base/`, anders draait selfHeal de
   fix steeds terug.
3. Het dashboard springt op rood, met een concrete fix per bevinding. De alert gaat af.
4. Fix het live (PDB → `maxUnavailable: 1`). De blocker verdwijnt en de score stijgt.

## Werkverdeling
- Flex: Go-scanner + de koppeling met de OpenShift API
- Groepsgenoot 2: de metrics (vorm, labels, hoe de tekst eruit komt)
- Groepsgenoot 3: Perses-dashboard + datasource
