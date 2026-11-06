# Cluster opzetten

Zo komt alles op een leeg cluster te staan. Vrijwel alles staat in Git; dit zijn de stappen die Argo CD niet zelf kan doen.

Voer de stappen op volgorde uit. Je hebt `oc` en `jq` nodig, en een account met cluster-admin.

1. **Alleen op CRC: zet de ingebouwde monitoring aan.** CRC schakelt die uit met overrides in `ClusterVersion`. Dit past slecht in Argo CD: je haalt twee regels uit een bestaand object, en dat is alleen op CRC nodig.

   ```bash
   oc get clusterversion version -o json | jq -c '{spec:{overrides:[.spec.overrides[] | select(.name != "cluster-monitoring-operator" and .name != "monitoring")]}}' | oc patch clusterversion version --type=merge --patch-file=/dev/stdin
   ```

   Wacht tot de monitoring bestaat en beschikbaar is. Dit zijn twee commando's, omdat `oc wait --for=condition` meteen met `NotFound` stopt zolang het object er nog niet is:

   ```bash
   oc wait co/monitoring --for=create --timeout=15m
   ```

   ```bash
   oc wait co/monitoring --for=condition=Available --timeout=15m
   ```

2. **Installeer Argo CD.**

   ```bash
   oc apply -f bootstrap/gitops-operator.yaml
   ```

   Wacht tot namespace `openshift-gitops` en de Application-CRD bestaan:

   ```bash
   oc wait namespace/openshift-gitops --for=create --timeout=10m
   ```

   ```bash
   oc wait crd/applications.argoproj.io --for=create --timeout=10m
   ```

   ```bash
   oc wait crd/applications.argoproj.io --for=condition=Established --timeout=10m
   ```

3. **Geef Argo CD zijn rechten en de twee Applications.**

   ```bash
   oc apply -f argocd/
   ```

   Vanaf hier rolt Argo CD `cluster/` en `overlays/prod` uit. De eerste sync kan een paar keer opnieuw starten, omdat de CRD's van de Cluster Observability Operator nog niet bestaan. Beide Applications proberen het tot 10 keer opnieuw. Staat een app daarna nog niet op `Synced`, klik dan op **Sync** op de webpagina van Argo CD.

4. **Bouw het eerste image van de exporter.** Tot die tijd staat de pod op `ImagePullBackOff`. Wacht eerst tot Argo CD de BuildConfig heeft aangemaakt:

   ```bash
   oc wait buildconfig/upgrade-readiness-exporter -n clusterbusters --for=create --timeout=20m
   ```

   ```bash
   oc start-build upgrade-readiness-exporter -n clusterbusters --follow
   ```

   ```bash
   oc rollout restart deployment/upgrade-readiness-exporter -n clusterbusters
   ```

## Controleren

```bash
oc get applications.argoproj.io -n openshift-gitops
```

Beide Applications horen op `Synced` en `Healthy` te staan.

## Wat met de hand blijft

| Wat | Waarom |
| --- | --- |
| `bootstrap/gitops-operator.yaml` | Argo CD kan zichzelf niet installeren |
| `argocd/` | Iets moet Argo CD de eerste keer vertellen wat het moet uitrollen |
| Overrides in `ClusterVersion` | Alleen op CRC, en het is geen eigen object |
| `oc start-build` | Er is geen webhook; zie `exporter/README.md` |

Een bestand weghalen uit `cluster/` verwijdert niets van het cluster: de Application `clusterbusters-cluster` prunet bewust niet.
