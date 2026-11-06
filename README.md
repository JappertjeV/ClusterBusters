# ClusterBusters: OpenShift Upgrade Readiness

Is this OpenShift cluster ready for the next upgrade? ClusterBusters checks the cluster automatically
every minute and shows, for each problem, what you must fix before you upgrade. That means fewer
failed upgrades.

Built for the KPN hackathon (5 and 6 November 2026) by a team of three.

## What it checks

| Check | What goes wrong during an upgrade |
|---|---|
| `clusterversion` | The cluster itself reports `Failing` or `Upgradeable=False`, an update is already running, or an admin gate is not acknowledged |
| `operators` | A platform component (ClusterOperator) is not Available, is Degraded or blocks upgrades |
| `olm` | An installed Operator does not work, does not support the new version, or its update is stuck or waiting for approval |
| `nodes` | A node is not Ready, has memory or disk pressure, or a MachineConfigPool is Degraded or paused |
| `pdb` | A PodDisruptionBudget allows no evictions and makes the node drain hang |
| `deprecated_apis` | API versions are in use that disappear in the new version |
| `unsupported` | Unsupported settings (`unsupportedConfigOverrides`) or standalone pods without a controller exist |

Each finding gets a severity (**blocker**, **warning** or **info**), an explanation and a concrete fix.
The score is 100 − 25 per blocker − 5 per warning, minimum 0. "Ready for upgrade" means: no blockers
and all checks succeeded.

### Works for every OpenShift version

No OpenShift version is hard-coded. On every scan the exporter reads the current version from
`ClusterVersion` and assesses against the next minor: on 4.17 the target is 4.18, and after the upgrade
to 4.18 it becomes 4.19 by itself. It calculates what depends on the version:

- which deprecated APIs disappear in the target version (Kubernetes 1.N belongs to OpenShift 4.(N−13));
- whether an Operator supports the target version (`olm.maxOpenShiftVersion`);
- which admin gates and known update risks the cluster itself reports;
- the suggested update channel (`stable-<current minor>`).

To assess against another version, for example a jump from 4.16 to 4.18, set `TARGET_VERSION` in
`base/exporter-deployment.yaml` and merge that. Perses and the KPN page show the current and target
version (metric `upgrade_readiness_version_info`).

## How it works

```
OpenShift API ──► Go exporter ──► /metrics ──scrape──► Prometheus ──► Thanos Querier ──► Perses dashboard
                      │                                     └──► PrometheusRule ──► Alertmanager
                      └──► /  (KPN dashboard page, live data through /api/status)
```

- The **exporter** (Go) reads the cluster through the Kubernetes API and exposes the findings as
  Prometheus metrics. It writes nowhere: Prometheus fetches the metrics itself.
- OpenShift **user workload monitoring** scrapes the exporter. The **Thanos Querier** combines that
  data with the platform metrics.
- **Perses** (in the OpenShift console) and the **KPN page** (served by the exporter itself) show the
  result. Blockers trigger an **alert**.
- **Argo CD** rolls everything out from this repository.

## Opening the dashboards

### KPN dashboard page (recommended)

The exporter serves a dashboard in KPN house style: overview, fixes, score over time, check status and
raw platform metrics. The page refreshes every 15 seconds. It is in English by default; the EN/NL
switch at the top changes it to Dutch and your browser remembers the choice.

1. Get the address:

   ```bash
   oc get route upgrade-readiness -n clusterbusters
   ```

2. Open the host from the `HOST/PORT` column in your browser, with `https://` in front of it. On CRC you
   get a certificate warning, because the cluster uses its own certificate: click "Advanced" and then
   "Proceed".

If the Route cannot be reached from your computer (possible with an externally hosted CRC), use a
port-forward. Keep this window open and go to http://localhost:8080:

```bash
oc port-forward -n clusterbusters deploy/upgrade-readiness-exporter 8080:8080
```

For the first minute after a restart the page says "The first scan is still running". The page has no
login: anyone who can reach the Route can see the findings.

### Perses dashboard (in the OpenShift console)

The official dashboard, and the fallback if the page above does not work. It is in English only. In the
OpenShift console, open **Observe → Dashboards (Perses)**, choose project `clusterbusters` and dashboard
**ClusterBusters: OpenShift Upgrade Readiness**.

### Alerts and individual metrics

- Alerts: **Observe → Alerting** in the console. `UpgradeBlockersPresent` fires when the cluster has not
  been ready for 2 minutes, `UpgradeReadinessCheckFailing` when a check fails,
  `UpgradeReadinessScanStale` when no scan has completed for 10 minutes, and
  `UpgradeReadinessMetricsMissing` when no metrics have arrived for 10 minutes. The alert texts are in
  Dutch.
- Metrics: **Observe → Metrics**, for example `upgrade_readiness_score` or
  `upgrade_readiness_finding == 1`.

## Deploying to a cluster

The full procedure for an empty cluster is in [`docs/cluster-opzetten.md`](docs/cluster-opzetten.md)
(in Dutch). In short, with cluster-admin:

1. **CRC only:** enable the built-in monitoring (CRC disables it by default).
2. **Install Argo CD** (OpenShift GitOps):

   ```bash
   oc apply -f bootstrap/gitops-operator.yaml
   ```

3. **Give Argo CD its permissions and the Applications:**

   ```bash
   oc apply -f argocd/
   ```

   Argo CD then rolls out `cluster/` (Cluster Observability Operator, Perses in the console, monitoring
   config) and `overlays/prod` (exporter, dashboard, alerts, Route) in namespace `clusterbusters`.

4. **Build the exporter image** in the cluster and start it:

   ```bash
   oc start-build upgrade-readiness-exporter -n clusterbusters --follow
   ```

   ```bash
   oc rollout restart deployment/upgrade-readiness-exporter -n clusterbusters
   ```

5. **Check** that both Applications are `Synced` and `Healthy`, and that the exporter is scanning:

   ```bash
   oc get applications.argoproj.io -n openshift-gitops
   ```

   ```bash
   oc logs -n clusterbusters deploy/upgrade-readiness-exporter --tail=5
   ```

   A line `scan klaar … score=…` should appear every minute.

### Rolling out a new version

Always change things through Git: Argo CD reverts manual changes on the cluster (`selfHeal`).

- **YAML** in `base/`, `overlays/` or `cluster/`: merge to `main` and Argo CD rolls it out within a few
  minutes.
- **Go code or the page** in `exporter/`: merge to `main`, then rebuild and restart. There is no
  automatic build.

  ```bash
  oc start-build upgrade-readiness-exporter -n clusterbusters --follow
  ```

  ```bash
  oc rollout restart deployment/upgrade-readiness-exporter -n clusterbusters
  ```

## Demo and testing

[`testcases/`](testcases/README.md) holds files you apply by hand. They are deliberately outside `base/`,
because Argo CD would otherwise revert every live fix.

- `demo-kapot.yaml`: the short demo scenario. One blocker and two warnings; the score drops by 35 points
  and rises by 25 after the live fix. On `crc03` that goes from 70 to 35 and then to 60: the CRC
  overrides and the deprecated DeploymentConfig API already cost 30 points there. As a result,
  `UpgradeBlockersPresent` is already firing there before the demo.
- `testomgeving.yaml`: good and broken parts side by side (workloads, PDBs, a standalone pod, a client
  of a deprecated API, stuck Operator updates).
- `nep-clusteroperator.yaml` (optional, cluster-admin): a fake ClusterOperator that reports Degraded,
  without breaking the real platform. Remove it afterwards.

Demo in four steps:

```bash
oc apply -f https://raw.githubusercontent.com/JappertjeV/ClusterBusters/main/testcases/demo-kapot.yaml
```

Wait 1 to 2 minutes: the dashboard turns red, with a fix for each finding. Then fix the blocker live:

```bash
oc patch pdb demo-app -n clusterbusters-demo --type=merge -p "spec: {minAvailable: null, maxUnavailable: 1}"
```

The blocker disappears and the score rises. Clean up:

```bash
oc delete namespace clusterbusters-demo
```

Do not run the demo at the same time as the test environment: with too many findings the score stays at
0 and you will not see it rise after a fix.

Test the Go code locally, without a cluster:

```bash
cd exporter && go test ./...
```

## Repository

```
argocd/          Argo CD Applications for overlays/prod and cluster/, plus Argo's permissions on UIPlugins
base/            Kustomize base: exporter (build, deployment, permissions, alerts, Route), Perses dashboard and datasource
overlays/prod/   namespace clusterbusters and the labels
cluster/         cluster-wide: Cluster Observability Operator, UIPlugin for Perses, monitoring config
bootstrap/       OpenShift GitOps Operator (by hand: Argo CD cannot install itself)
exporter/        the Go exporter and the KPN page (see exporter/README.md)
testcases/       demo and test environment (apply by hand)
docs/            cluster setup
CLAUDE.md        project context for Claude Code
```

## Tools and technology

| Part | Tool |
|---|---|
| Platform | OpenShift (CRC, single-node, externally hosted) |
| Rollout | Argo CD through OpenShift GitOps, Kustomize |
| Exporter | Go, with `client-go` (dynamic client) and `prometheus/client_golang` |
| Image | Red Hat UBI (go-toolset to build, ubi-micro to run), built with an OpenShift BuildConfig |
| Metrics and alerts | OpenShift user workload monitoring (Prometheus), Thanos Querier, PrometheusRule, Alertmanager |
| Dashboard | Perses through the Cluster Observability Operator (UIPlugin in the console) |
| KPN page | HTML, CSS and JavaScript without a framework, embedded in the exporter with `go:embed`; colours and typeface from the public KPN design tokens |
| Verification | `go test`, `kustomize build`, `kubeconform`, `percli lint` |
| Way of working | GitHub with pull requests, Claude Code as a programming aid |

## How we built it

1. **Set up Argo CD** (PR #1 to #4): an Application that rolls out `overlays/prod` to namespace
   `clusterbusters`, first tested with a test app.
2. **Monitoring and first dashboard** (PR #5 to #7): enabled user workload monitoring, a Perses dashboard
   on the platform metrics, and the Perses datasource with TLS to the Thanos Querier.
3. **Choice for the built-in monitoring:** a separate MonitoringStack duplicated the platform Prometheus
   and was removed.
4. **The Go exporter** (PR #8 and #9): seven checks, the metrics, BuildConfig, Deployment, permissions and
   alerts. The image is built in the cluster.
5. **Dashboard on the exporter and the demo** (PR #10): the panels show the exporter's verdicts, with the
   demo in `testcases/`.
6. **Deprecated APIs** (PR #11): `APIRequestCount` was empty on CRC, so the check now also reads the
   platform metric through Thanos.
7. **Everything through Git** (PR #13): the Operators and `cluster/` also go through Argo CD; only
   installing Argo CD itself remains manual.
8. **House style and expansion** (PR #12, #14): the Perses dashboard got the KPN colours, the exporter now
   also sees stuck Operator updates, and a test environment with good and broken parts was added.
9. **Own KPN page** (PR #15): Perses can only partly render the KPN design, so the exporter serves the
   design itself, with live data.
10. **Perses cleaned up** (PR #16): Perses remains the fallback, without the parts that did not work well
    in the console.

## Known limitations

- CRC is single-node and is not really upgraded: we assess the *readiness*, nothing gets upgraded.
- The KPN page has no login.
- The score chart on the KPN page only keeps the last 180 scans in memory and starts over after a
  restart. The full history is in Prometheus and Perses.
- After new Go code you must run `oc start-build` yourself: there is no webhook.
- Perses sorts the table of fixes alphabetically by severity (blocker, info, warning); the KPN page puts
  blockers at the top.
- Text that comes from the cluster itself, such as the message of a Degraded condition, stays in English
  when the KPN page is set to Dutch.
