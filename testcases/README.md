# Demo: van rood naar beter

Voer deze stappen op volgorde uit. De exporter scant elke 60 seconden en wordt elke 60 seconden gescrapet, dus reken op maximaal 2 minuten per stap.

1. Maak de problemen aan:

   ```bash
   oc apply -f testcases/demo-kapot.yaml
   ```

   Het dashboard toont 1 blocker en 2 warnings extra, met per bevinding de oplossing. De score daalt met 35 punten.

2. Fix de blocker live:

   ```bash
   oc patch pdb demo-app -n clusterbusters-demo --type=json -p '[{"op":"remove","path":"/spec/minAvailable"},{"op":"add","path":"/spec/maxUnavailable","value":1}]'
   ```

   De blocker verdwijnt en de score stijgt met 25 punten.

3. Ruim de demo op:

   ```bash
   oc delete namespace clusterbusters-demo
   ```
