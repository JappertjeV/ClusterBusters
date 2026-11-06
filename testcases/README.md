# Testcases

Alles in deze map pas je met de hand toe. Het staat bewust buiten `base/`: anders beheert Argo CD
het en draait selfHeal elke live fix terug.

De exporter scant elke 60 seconden en wordt elke 60 seconden gescrapet. Reken op maximaal 2 minuten
voordat een wijziging op het dashboard staat.

Alle commando's werken zonder lokale kopie van de repo: `oc apply` leest de bestanden van GitHub.
Ze werken ook in de Windows-opdrachtprompt en PowerShell: de patches zijn YAML zonder aanhalingstekens
erin. Alleen de status-patch van de nep-ClusterOperator vraagt een lokale kopie (zie daar).

## Demo: van rood naar beter

Kort scenario voor de presentatie.

1. Maak de problemen aan:

   ```bash
   oc apply -f https://raw.githubusercontent.com/JappertjeV/ClusterBusters/main/testcases/demo-kapot.yaml
   ```

   Het dashboard toont 1 blocker en 2 warnings extra, met per bevinding de oplossing. De score daalt met 35 punten.

2. Fix de blocker live:

   ```bash
   oc patch pdb demo-app -n clusterbusters-demo --type=merge -p "spec: {minAvailable: null, maxUnavailable: 1}"
   ```

   De blocker verdwijnt en de score stijgt met 25 punten.

3. Ruim de demo op:

   ```bash
   oc delete namespace clusterbusters-demo
   ```

## Testomgeving: goed en kapot naast elkaar

`testomgeving.yaml` zet een mix neer in de namespaces `clusterbusters-test`,
`clusterbusters-test-olm-kapot` en `clusterbusters-test-olm-wacht`. Hiermee laat je zien dat de
exporter het verschil ziet tussen goed en kapot. Er is geen cluster-admin voor nodig.

```bash
oc apply -f https://raw.githubusercontent.com/JappertjeV/ClusterBusters/main/testcases/testomgeving.yaml
```

| Onderdeel | Wat het is | Verwacht op het dashboard |
|---|---|---|
| `goed-web` | 2 replicas, PDB `maxUnavailable: 1` | niets |
| `goed-api` | HTTP-API (httpd), 2 replicas, Service, PDB `minAvailable: 1` | niets |
| `kapot-een-replica` | 1 replica, PDB `minAvailable: 1` | **blocker** (pdb) |
| `kapot-max-nul` | 3 replicas, PDB `maxUnavailable: 0` | **blocker** (pdb) |
| `kapot-weesje` | PDB zonder pods | warning (pdb) |
| `kapot-losse-pod` | pod zonder Deployment | warning (unsupported) |
| `kapot-oude-api-client` | roept elke 30 s de verouderde DeploymentConfig-API aan | warning (deprecated_apis), met deze ServiceAccount als gebruiker |
| `kapot-bestaat-niet` | Subscription op een Operator die niet bestaat | warning (olm): `ResolutionFailed` |
| `kapot-wacht-op-goedkeuring` | Subscription met `installPlanApproval: Manual` | warning (olm): update wacht op goedkeuring |

Samen ongeveer 2 blockers en 5 warnings: de score zakt met zo'n 75 punten. Stond de
DeploymentConfig-API al in de lijst, dan komt er bij `deprecated_apis` niets bij.

Keur de InstallPlan in `clusterbusters-test-olm-wacht` **niet** goed. Dan installeert OLM echt de
cert-manager-Operator. Bestaat dat package niet in de catalogus van het cluster, dan zie je daar
`ResolutionFailed`; ook dat is een warning.

Live fixen, één voor één:

```bash
oc patch pdb kapot-een-replica -n clusterbusters-test --type=merge -p "spec: {minAvailable: null, maxUnavailable: 1}"
```
```bash
oc patch pdb kapot-max-nul -n clusterbusters-test --type=merge -p "spec: {minAvailable: null, maxUnavailable: 1}"
```
```bash
oc delete pdb kapot-weesje -n clusterbusters-test
```
```bash
oc delete pod kapot-losse-pod -n clusterbusters-test
```
```bash
oc delete namespace clusterbusters-test-olm-kapot
```

De API-client geeft na het stoppen nog 24 uur een bevinding: zo lang onthoudt APIRequestCount de aanroepen.

Opruimen:

```bash
oc delete -f https://raw.githubusercontent.com/JappertjeV/ClusterBusters/main/testcases/testomgeving.yaml
```

## Optioneel: nep-ClusterOperator (cluster-admin)

Platform-onderdelen maak je liever niet echt kapot. Deze nep-ClusterOperator meldt zich `Degraded`
en `Upgradeable=False`. Er draait niets achter, het is alleen een statusobject.

```bash
oc apply -f https://raw.githubusercontent.com/JappertjeV/ClusterBusters/main/testcases/nep-clusteroperator.yaml
```
Deze patch leest een bestand, dus voer hem uit in de map van je lokale kopie van de repo, na `git pull`:

```bash
oc patch clusteroperator clusterbusters-nep --subresource=status --type=merge --patch-file=testcases/nep-clusteroperator-status.yaml
```

Verwacht:
- **operators:** 2 blockers voor `ClusterOperator clusterbusters-nep` (Degraded en Upgradeable=False).
- **Platform-panelen:** "Unhealthy Cluster Operators" gaat naar 1.
- **clusterversion:** waarschijnlijk zet de Cluster Version Operator na een paar minuten ook
  `Upgradeable=False` op de ClusterVersion. Dat is precies de keten die je wilt laten zien: één
  operator houdt de hele cluster-upgrade tegen.

Ruim hem daarna **altijd** op, want zolang hij bestaat meldt het cluster zich als niet-upgradeable:

```bash
oc delete clusteroperator clusterbusters-nep
```
