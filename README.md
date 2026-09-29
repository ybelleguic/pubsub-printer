# pubsub-printer

Consommateur Go d’une souscription Pub/Sub. Chaque message est écrit sur stdout, avec ses métadonnées. Les journaux du processus vont sur stderr.

Le client utilise les bibliothèques Google Cloud. Sur un univers autre que `googleapis.com`, l’API est jointe sur `pubsub.<univers>` : pour Cloud de Confiance, `pubsub.s3nsapis.fr`. Sur GKE et Cloud Run, l’authentification passe par Application Default Credentials et le metadata server (Workload Identity, ou le compte de service du service Cloud Run). Aucun fichier de clé n’est nécessaire dans ces deux cas.

## Configuration

L’ordre de priorité est : flags, variables d’environnement, fichier JSON, valeurs par défaut.

```bash
pubsub-printer --config config.json
pubsub-printer --project s3ns:my-project --subscription events --universe-domain s3nsapis.fr
```

Fichier d’exemple : [`config.example.json`](config.example.json).

| Réglage | Flag | Environnement | JSON |
| --- | --- | --- | --- |
| Projet | `--project` | `PUBSUB_PROJECT_ID`, sinon `GOOGLE_CLOUD_PROJECT` | `project_id` |
| Souscription | `--subscription` | `PUBSUB_SUBSCRIPTION` | `subscription` |
| Univers | `--universe-domain` | `PUBSUB_UNIVERSE_DOMAIN`, sinon `GOOGLE_CLOUD_UNIVERSE_DOMAIN` | `universe_domain` |
| Endpoint | `--endpoint` | `PUBSUB_ENDPOINT` | `endpoint` |
| Credentials | `--credentials` | `PUBSUB_CREDENTIALS_FILE`, sinon `GOOGLE_APPLICATION_CREDENTIALS` | `credentials_file` |
| Projet de quota | `--quota-project` | `PUBSUB_QUOTA_PROJECT`, sinon `GOOGLE_CLOUD_QUOTA_PROJECT` | `quota_project` |
| Format | `--format` | `PUBSUB_FORMAT` | `format` (`pretty` ou `json`) |
| Ack | `--ack` | `PUBSUB_ACK` | `ack` |
| Couleur | `--color` | `PUBSUB_COLOR` | `color` (`auto`, `always`, `never`) |
| Messages en vol | `--max-outstanding-messages` | `PUBSUB_MAX_OUTSTANDING_MESSAGES` | `max_outstanding_messages` |
| Octets en vol | `--max-outstanding-bytes` | `PUBSUB_MAX_OUTSTANDING_BYTES` | `max_outstanding_bytes` |
| Goroutines | `--num-goroutines` | `PUBSUB_NUM_GOROUTINES` | `num_goroutines` |
| Troncature affichée | `--max-body-bytes` | `PUBSUB_MAX_BODY_BYTES` | `max_body_bytes` (`0` = pas de troncature) |
| Exactly-once | `--exactly-once` | `PUBSUB_EXACTLY_ONCE` | `exactly_once` |
| Santé HTTP | `--health-addr` | `HEALTH_ADDR` | `health_addr` |
| Fichier de config | `--config` | `PUBSUB_CONFIG` | |
| Émulateur | `--emulator-host` | `PUBSUB_EMULATOR_HOST` | `emulator_host` |

La souscription peut être un identifiant court (`events`) ou le nom complet `projects/{projet}/subscriptions/{id}`. Si seul le nom complet est fourni, le projet est déduit du chemin.

Quand `PORT` est défini et qu’aucune adresse de santé n’est choisie, le processus écoute sur `:$PORT`. C’est le cas d’un service Cloud Run. `--health-addr=-` (ou `HEALTH_ADDR=-`) désactive ce serveur. Les sondes sont `GET /healthz` (processus vivant) et `GET /readyz` (boucle de réception démarrée).

`--ack=false` affiche le message puis le nack, ce qui provoque une redelivery. La valeur par défaut acquitte le message après l’écriture sur stdout.

## Sortie

`pretty` (défaut) écrit un bloc par message. Le corps JSON est indenté sous le bloc pour rester copiable. `json` écrit une ligne JSON par message, adaptée à un pipe.

Les deux formats portent le projet, le nom complet de la souscription, l’univers, l’endpoint, l’identifiant, les dates de publication et de réception, la clé d’ordre, la tentative de livraison (quand la souscription a une dead-letter policy), les attributs triés, et le corps.

```text
┌─ 18446744073709551615
│ project        s3ns:demo
│ subscription   projects/s3ns:demo/subscriptions/events
│ universe       s3nsapis.fr
│ endpoint       pubsub.s3nsapis.fr:443
│ published      2026-09-29T11:49:58.000Z
│ received       2026-09-29T11:49:59.000Z
│ ordering_key   user-42
│ attempt        2
│ attributes
│   event          user.created
│   source         api
│ payload        29 B · json
└─
{
  "hello": "s3ns",
  "user": "ada"
}
```

## Univers GCP

Hors de `googleapis.com`, les bibliothèques doivent connaître l’univers. Pour S3NS :

```bash
export GOOGLE_CLOUD_UNIVERSE_DOMAIN=s3nsapis.fr
```

Le processus exporte aussi cette variable à partir de `universe_domain` avant d’ouvrir le client, pour que l’hôte d’API et les credentials racontent la même chose. L’endpoint devient `pubsub.s3nsapis.fr:443`, sauf si `endpoint` est renseigné. Les identifiants de projet Cloud de Confiance portent le préfixe d’univers, par exemple `s3ns:my-project`.

Le compte qui consomme la souscription a besoin de `roles/pubsub.subscriber`.

## Exécution locale

```bash
go build -o bin/pubsub-printer ./cmd/pubsub-printer
./bin/pubsub-printer --project s3ns:my-project --subscription events --universe-domain s3nsapis.fr
```

En local, les Application Default Credentials viennent de `gcloud auth application-default login` (univers inclus) ou d’un fichier JSON pointé par `--credentials`. Sur GKE et Cloud Run, laisser ce champ vide.

L’émulateur Pub/Sub reste disponible pour un essai sans API :

```bash
gcloud beta emulators pubsub start --host-port=127.0.0.1:8085
PUBSUB_EMULATOR_HOST=127.0.0.1:8085 \
  ./bin/pubsub-printer --project demo-project --subscription events
```

## GKE et Workload Identity

Le manifeste [`deploy/gke/deployment.yaml`](deploy/gke/deployment.yaml) associe le compte de service Kubernetes `pubsub-printer` au compte Google via l’annotation `iam.gke.io/gcp-service-account`. Le cluster doit avoir Workload Identity. Ne pas monter de clé JSON et ne pas définir `GOOGLE_APPLICATION_CREDENTIALS` : le client interroge le metadata server, qui fournit à la fois le jeton et le domaine d’univers.

```bash
gcloud iam service-accounts create pubsub-printer
gcloud pubsub subscriptions add-iam-policy-binding events \
  --member="serviceAccount:pubsub-printer@PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/pubsub.subscriber"
gcloud iam service-accounts add-iam-policy-binding \
  pubsub-printer@PROJECT_ID.iam.gserviceaccount.com \
  --role="roles/iam.workloadIdentityUser" \
  --member="serviceAccount:PROJECT_ID.svc.id.goog[NAMESPACE/pubsub-printer]"
```

Sur un univers S3NS, configurer d’abord `gcloud` avec `universe_domain=s3nsapis.fr` et utiliser le projet tel qu’il apparaît dans la console (`s3ns:...` le cas échéant).

## Cloud Run

Le compte de service attaché au service ou au worker pool joue le même rôle que Workload Identity : ADC via le metadata server, sans fichier de clé.

Un **worker pool** est le bon format pour une boucle StreamingPull, parce qu’il n’attend pas de trafic HTTP :

```bash
gcloud run worker-pools deploy pubsub-printer \
  --image REGION-docker.pkg.dev/PROJECT_ID/REPO/pubsub-printer:latest \
  --region REGION \
  --service-account pubsub-printer@PROJECT_ID.iam.gserviceaccount.com \
  --set-env-vars PUBSUB_PROJECT_ID=s3ns:my-project,PUBSUB_SUBSCRIPTION=events,GOOGLE_CLOUD_UNIVERSE_DOMAIN=s3nsapis.fr
```

Un **service** Cloud Run doit écouter `$PORT`. L’application le fait dès que `PORT` est présent. Il faut en plus garder le CPU alloué et au moins une instance, sinon le pull s’arrête entre deux requêtes. Le manifeste [`deploy/cloudrun/service.yaml`](deploy/cloudrun/service.yaml) porte `run.googleapis.com/cpu-throttling=false` et `minScale=1`. Pour que Cloud Logging garde un message par entrée, définir `PUBSUB_FORMAT=json` : le format `pretty` est multi-ligne.

```bash
gcloud run deploy pubsub-printer \
  --image REGION-docker.pkg.dev/PROJECT_ID/REPO/pubsub-printer:latest \
  --region REGION \
  --service-account pubsub-printer@PROJECT_ID.iam.gserviceaccount.com \
  --no-cpu-throttling \
  --min-instances 1 \
  --max-instances 1 \
  --timeout 3600 \
  --set-env-vars PUBSUB_PROJECT_ID=s3ns:my-project,PUBSUB_SUBSCRIPTION=events,GOOGLE_CLOUD_UNIVERSE_DOMAIN=s3nsapis.fr
```

Arrêter le conteneur envoie SIGTERM. Le processus cesse de tirer des messages, termine les callbacks en cours, puis sort.

## Image

```bash
docker build -t pubsub-printer .
```

L’image finale est `scratch` avec le bundle de certificats. Elle ne dépend pas de `gcr.io`, souvent inaccessible depuis un univers partenaire. Le binaire tourne avec l’uid `65532`.

## Développement

Go 1.25 ou plus récent.

```bash
go test ./...
```
