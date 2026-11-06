# ClusterBusters: OpenShift Upgrade Readiness

Is dit OpenShift-cluster klaar voor de volgende upgrade? ClusterBusters controleert het cluster
elke minuut automatisch en laat per probleem zien wat je moet fixen voordat je upgradet. Zo mislukken
er minder upgrades.

Gebouwd voor de KPN-hackathon (5 en 6 november 2026) door een team van drie.

## Wat het controleert

| Check | Wat er misgaat bij een upgrade |
|---|---|
| `clusterversion` | Het cluster meldt zelf `Failing` of `Upgradeable=False`, een update loopt al, of een admin-gate is niet bevestigd |
| `operators` | Een platform-onderdeel (ClusterOperator) is niet Available, is Degraded of blokkeert upgrades |
| `olm` | Een geïnstalleerde Operator werkt niet, ondersteunt de nieuwe versie niet, of zijn update zit vast of wacht op goedkeuring |
| `nodes` | Een node is niet Ready, heeft geheugen- of schijfdruk, of een MachineConfigPool is Degraded of gepauzeerd |
| `pdb` | Een PodDisruptionBudget staat geen enkele evictie toe en laat de node-drain vastlopen |
| `deprecated_apis` | Er worden API-versies gebruikt die in de nieuwe versie verdwijnen |
| `unsupported` | Er staan niet-ondersteunde instellingen (`unsupportedConfigOverrides`) of losse pods zonder controller |

Elke bevinding krijgt een ernst (**blocker**, **warning** of **info**), een uitleg en een concrete fix.
De score is 100 − 25 per blocker − 5 per warning, minimaal 0. "Klaar voor upgrade" betekent: geen
blockers en alle checks gelukt.

### Werkt voor elke OpenShift-versie

Er staat nergens een OpenShift-versie vast. Bij elke scan leest de exporter de huidige versie uit de
`ClusterVersion` en beoordeelt hij tegen de minor daarna: op 4.17 is het doel 4.18, na de upgrade naar
4.18 vanzelf 4.19. Wat versie-afhankelijk is, rekent hij uit:

- welke verouderde API's in de doelversie verdwijnen (Kubernetes 1.N hoort bij OpenShift 4.(N−13));
- of een Operator de doelversie ondersteunt (`olm.maxOpenShiftVersion`);
- welke admin-gates en bekende update-risico's het cluster zelf meldt;
- het voorgestelde update-channel (`stable-<huidige minor>`).

Wil je tegen een andere versie beoordelen, bijvoorbeeld een sprong van 4.16 naar 4.18, zet dan
`TARGET_VERSION` in `base/exporter-deployment.yaml` en merge dat. Perses en de KPN-pagina tonen
welke versie nu en welke als doel gebruikt wordt (metric `upgrade_readiness_version_info`).

## Hoe het werkt

```
OpenShift API ──► Go-exporter ──► /metrics ──scrape──► Prometheus ──► Thanos Querier ──► Perses-dashboard
                      │                                     └──► PrometheusRule ──► Alertmanager
                      └──► /  (KPN-dashboardpagina, live data via /api/status)
```

- De **exporter** (Go) leest het cluster via de Kubernetes-API en zet de bevindingen als
  Prometheus-metrics klaar. Hij schrijft nergens naartoe: Prometheus haalt de metrics zelf op.
- **User workload monitoring** van OpenShift scrapet de exporter. De **Thanos Querier** voegt die
  data samen met de platform-metrics.
- **Perses** (in de OpenShift-console) en de **KPN-pagina** (geserveerd door de exporter zelf) tonen
  het resultaat. Bij blockers gaat een **alert** af.
- **Argo CD** rolt alles uit vanuit deze repository.

## De dashboards openen

### KPN-dashboardpagina (aanbevolen)

De exporter serveert zelf een dashboard in KPN-huisstijl: overzicht, fixes, score in de tijd,
status van de checks en ruwe platform-metrics. De pagina ververst elke 15 seconden.

1. Vraag het adres op:

   ```bash
   oc get route upgrade-readiness -n clusterbusters
   ```

2. Open de host uit de kolom `HOST/PORT` in je browser, met `https://` ervoor. Op CRC krijg je een
   certificaatwaarschuwing, omdat het cluster een eigen certificaat gebruikt: klik op "Geavanceerd" en
   dan "Doorgaan".

Is de Route niet bereikbaar vanaf je computer (bij een extern gehost CRC kan dat), gebruik dan een
port-forward. Laat dit venster open en ga naar http://localhost:8080:

```bash
oc port-forward -n clusterbusters deploy/upgrade-readiness-exporter 8080:8080
```

De eerste minuut na een herstart staat er "De eerste scan loopt nog". De pagina heeft geen login:
iedereen die de Route kan bereiken, ziet de bevindingen.

### Perses-dashboard (in de OpenShift-console)

Het officiële dashboard, en de fallback als de pagina hierboven niet werkt. Open in de
OpenShift-console **Observe → Dashboards (Perses)**, kies project `clusterbusters` en dashboard
**ClusterBusters: OpenShift Upgrade Readiness**.

### Alerts en losse metrics

- Alerts: **Observe → Alerting** in de console. `UpgradeBlockersPresent` gaat af als het cluster
  2 minuten niet klaar is, `UpgradeReadinessCheckFailing` als een check faalt,
  `UpgradeReadinessScanStale` als er 10 minuten geen scan is afgerond, en
  `UpgradeReadinessMetricsMissing` als er 10 minuten geen metrics binnenkomen.
- Metrics: **Observe → Metrics**, bijvoorbeeld `upgrade_readiness_score` of
  `upgrade_readiness_finding == 1`.

## Inzetten op een cluster

Het volledige stappenplan voor een leeg cluster staat in [`docs/cluster-opzetten.md`](docs/cluster-opzetten.md).
Kort samengevat, met cluster-admin:

1. **Alleen op CRC:** zet de ingebouwde monitoring aan (CRC schakelt die standaard uit).
2. **Installeer Argo CD** (OpenShift GitOps):

   ```bash
   oc apply -f bootstrap/gitops-operator.yaml
   ```

3. **Geef Argo CD zijn rechten en de Applications:**

   ```bash
   oc apply -f argocd/
   ```

   Argo CD rolt daarna `cluster/` (Cluster Observability Operator, Perses in de console, monitoring-config)
   en `overlays/prod` (exporter, dashboard, alerts, Route) uit in namespace `clusterbusters`.

4. **Bouw het image van de exporter** in het cluster en start hem:

   ```bash
   oc start-build upgrade-readiness-exporter -n clusterbusters --follow
   ```

   ```bash
   oc rollout restart deployment/upgrade-readiness-exporter -n clusterbusters
   ```

5. **Controleer** dat beide Applications op `Synced` en `Healthy` staan, en dat de exporter scant:

   ```bash
   oc get applications.argoproj.io -n openshift-gitops
   ```

   ```bash
   oc logs -n clusterbusters deploy/upgrade-readiness-exporter --tail=5
   ```

   Elke minuut hoort er een regel `scan klaar … score=…` te verschijnen.

### Een nieuwe versie uitrollen

Wijzig altijd via Git: Argo CD draait handmatige wijzigingen op het cluster terug (`selfHeal`).

- **YAML** in `base/`, `overlays/` of `cluster/`: merge naar `main`, Argo CD rolt het binnen een paar
  minuten uit.
- **Go-code of de pagina** in `exporter/`: merge naar `main`, bouw daarna opnieuw en herstart. Er is
  geen automatische build.

  ```bash
  oc start-build upgrade-readiness-exporter -n clusterbusters --follow
  ```

  ```bash
  oc rollout restart deployment/upgrade-readiness-exporter -n clusterbusters
  ```

## Demo en testen

In [`testcases/`](testcases/README.md) staan bestanden die je met de hand toepast. Ze staan bewust
buiten `base/`, anders draait Argo CD elke live fix terug.

- `demo-kapot.yaml`: het korte demo-scenario. Eén blocker en twee warnings; de score daalt met 35
  punten en stijgt na de live fix met 25. Op `crc03` gaat dat van 70 naar 35 en dan naar 60: de
  CRC-overrides en de verouderde DeploymentConfig-API kosten daar al 30 punten. Daardoor staat
  `UpgradeBlockersPresent` daar al aan vóór de demo.
- `testomgeving.yaml`: goede en kapotte onderdelen naast elkaar (workloads, PDB's, een losse pod,
  een client van een verouderde API, vastgelopen Operator-updates).
- `nep-clusteroperator.yaml` (optioneel, cluster-admin): een nep-ClusterOperator die zich Degraded
  meldt, zonder het echte platform kapot te maken. Verwijder hem na afloop.

Demo in vier stappen:

```bash
oc apply -f https://raw.githubusercontent.com/JappertjeV/ClusterBusters/main/testcases/demo-kapot.yaml
```

Wacht 1 à 2 minuten: het dashboard springt op rood, met per bevinding de fix. Fix daarna de blocker live:

```bash
oc patch pdb demo-app -n clusterbusters-demo --type=merge -p "spec: {minAvailable: null, maxUnavailable: 1}"
```

De blocker verdwijnt en de score stijgt. Ruim op:

```bash
oc delete namespace clusterbusters-demo
```

Draai de demo niet tegelijk met de testomgeving: met te veel bevindingen staat de score op 0 en zie je
hem bij een fix niet stijgen.

De Go-code test je lokaal, zonder cluster:

```bash
cd exporter && go test ./...
```

## Repository

```
argocd/          Argo CD-Applications voor overlays/prod en cluster/, plus Argo's rechten op UIPlugins
base/            Kustomize-base: exporter (build, deployment, rechten, alerts, Route), Perses-dashboard en -datasource
overlays/prod/   namespace clusterbusters en de labels
cluster/         clusterbreed: Cluster Observability Operator, UIPlugin voor Perses, monitoring-config
bootstrap/       OpenShift GitOps Operator (met de hand: Argo CD kan zichzelf niet installeren)
exporter/        de Go-exporter en de KPN-pagina (zie exporter/README.md)
testcases/       demo en testomgeving (met de hand toepassen)
docs/            cluster opzetten
CLAUDE.md        projectcontext voor Claude Code
```

## Gebruikte tools en technologie

| Onderdeel | Tool |
|---|---|
| Platform | OpenShift (CRC, single-node, extern gehost) |
| Uitrol | Argo CD via OpenShift GitOps, Kustomize |
| Exporter | Go, met `client-go` (dynamic client) en `prometheus/client_golang` |
| Image | Red Hat UBI (go-toolset om te bouwen, ubi-micro om te draaien), gebouwd met een OpenShift BuildConfig |
| Metrics en alerts | OpenShift user workload monitoring (Prometheus), Thanos Querier, PrometheusRule, Alertmanager |
| Dashboard | Perses via de Cluster Observability Operator (UIPlugin in de console) |
| KPN-pagina | HTML, CSS en JavaScript zonder framework, ingebouwd in de exporter met `go:embed`; kleuren en lettertype uit de openbare KPN-design-tokens |
| Controle | `go test`, `kustomize build`, `kubeconform`, `percli lint` |
| Werkwijze | GitHub met pull requests, Claude Code als programmeerhulp |

## Hoe we het gebouwd hebben

1. **Argo CD opgezet** (PR #1 t/m #4): een Application die `overlays/prod` uitrolt naar namespace
   `clusterbusters`, eerst getest met een testapp.
2. **Monitoring en eerste dashboard** (PR #5 t/m #7): user workload monitoring aangezet, een
   Perses-dashboard op de platform-metrics, en de Perses-datasource met TLS naar de Thanos Querier.
3. **Keuze voor de ingebouwde monitoring:** een losse MonitoringStack deed dubbel werk met de
   platform-Prometheus en is verwijderd.
4. **De Go-exporter** (PR #8 en #9): zeven checks, de metrics, BuildConfig, Deployment, rechten en
   alerts. Het image wordt in het cluster gebouwd.
5. **Dashboard op de exporter en de demo** (PR #10): de panelen tonen de oordelen van de exporter,
   met de demo in `testcases/`.
6. **Verouderde API's** (PR #11): op CRC was `APIRequestCount` leeg, dus de check leest nu ook de
   platform-metric via Thanos.
7. **Alles via Git** (PR #13): ook de Operators en `cluster/` lopen via Argo CD; alleen het
   installeren van Argo CD zelf blijft met de hand.
8. **Huisstijl en uitbreiding** (PR #12, #14): het Perses-dashboard kreeg de KPN-kleuren, de exporter
   ziet nu ook vastgelopen Operator-updates, en er kwam een testomgeving met goede en kapotte onderdelen.
9. **Eigen KPN-pagina** (PR #15): Perses kan het KPN-ontwerp maar deels weergeven, dus de exporter
   serveert het ontwerp zelf, met live data.
10. **Perses opgeruimd** (PR #16): Perses blijft de fallback, zonder de onderdelen die in de console
    niet goed werkten.

## Bekende beperkingen

- CRC is single-node en wordt niet echt geüpgraded: we beoordelen de *readiness*, er wordt niets
  geüpgraded.
- De KPN-pagina heeft geen login.
- De scoregrafiek op de KPN-pagina onthoudt alleen de laatste 180 scans in het geheugen en begint
  opnieuw na een herstart. De volledige historie staat in Prometheus en in Perses.
- Na nieuwe Go-code moet je zelf `oc start-build` draaien: er is geen webhook.
- Perses sorteert de tabel met fixes alfabetisch op ernst (blocker, info, warning); de KPN-pagina
  sorteert blockers bovenaan.
