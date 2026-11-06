# Zabbix Agent Exporter — Design

Date: 2026-11-06
Status: Approved for planning

## Contexte et objectif

Une flotte d'agents Zabbix existe sur des serveurs anciens (vieux OS) où
`node_exporter` ne peut pas être installé. L'objectif est de permettre la
décommission du serveur Zabbix central tout en continuant à superviser ces
hôtes via Prometheus.

Ce projet implémente un **exporter Prometheus qui agit comme un proxy** entre
Prometheus et les agents Zabbix existants : il parle le protocole Zabbix
agent passif pour interroger les agents à la demande, et expose le résultat
au format Prometheus. Il ne remplace pas le serveur Zabbix fonctionnellement
(pas de triggers, pas d'historique, pas de mode actif) — il se contente de
traduire des items Zabbix en métriques Prometheus, à la manière de
`blackbox_exporter` / `snmp_exporter`.

## Hors périmètre (MVP)

- Mode agent "actif" (agents qui poussent vers un serveur) — uniquement le
  mode passif (pull) est supporté.
- Triggers, actions, notifications Zabbix.
- Récupération de l'historique depuis l'ancien serveur Zabbix.
- TLS entre l'exporter et les agents (les anciens agents n'en disposent
  généralement pas).
- Hot-reload d'autre chose que le fichier de config (pas de reload du
  binaire lui-même).

## Architecture

Modèle "multi-target exporter à la demande", comme `blackbox_exporter` /
`snmp_exporter` : un seul binaire, déployé une fois, interrogé par Prometheus
via un paramètre `target` dans l'URL de scrape. Pas de scheduler ni de cache
interne — chaque requête HTTP déclenche un scrape synchrone des agents
concernés ; c'est Prometheus qui gère déjà l'intervalle de scrape.

```
cmd/zabbix_agent_exporter/main.go   # entrypoint, flags (--config), wiring
internal/config/                    # Viper: types, chargement, validation, watch (hot-reload)
internal/zabbixproto/               # protocole binaire ZBXD bas niveau (I/O pur, sans logique métier)
internal/zabbixclient/              # client haut niveau: GetItem(), Discover() (LLD), timeouts
internal/collector/                 # construit un prometheus.Collector pour un (target, modules[])
internal/httpserver/                # handlers /metrics (probe), /healthz, reload
configs/config.example.yaml         # exemple de config livré (module "linux" basique)
```

Pas de `prometheus.Collector` global enregistré au démarrage : pour chaque
requête `GET /metrics?target=...`, le serveur résout la config, construit un
`prometheus.Collector` ad hoc et un `prometheus.Registry` local temporaire
(pattern `probeHandler` de blackbox_exporter), puis sert le résultat.

## Protocole Zabbix agent (passif)

Format binaire (requêtes et réponses) :

```
+-------+-------+----------------------+----------------------+---------+
| "ZBXD"| flags | datalen (uint32 LE)  | reserved (uint32, 0)  | payload |
+-------+-------+----------------------+----------------------+---------+
  4B      1B(=0x01)      4B                      4B                N bytes
```

- **Requête** : payload = clé d'item en texte brut (ex:
  `system.cpu.load[,avg1]\n`). Le header ZBXD complet est envoyé ; en cas
  d'échec avec un vieil agent qui n'attend que du texte brut, un fallback
  sans header est tenté.
- **Réponse** : soit une valeur simple (texte), soit `ZBX_NOTSUPPORTED` +
  raison, soit un document JSON (pour les clés `*.discovery`, utilisées par
  LLD).
- **Connexion** : une connexion TCP par requête (ouverture, envoi, lecture
  jusqu'à `datalen` ou EOF, fermeture) — comportement natif d'un agent
  Zabbix passif, qui ferme après chaque réponse. Pas de pooling de
  connexions.
- **Timeout** : `net.DialTimeout` + `SetDeadline`, valeur par défaut 5s,
  surchageable (cascade décrite ci-dessous).
- **Erreurs réseau** (connexion refusée, timeout, EOF prématuré) : traitées
  comme un échec d'item, non fatales pour le reste du scrape — sauf le cas
  particulier du tout premier contact avec l'agent (cf. "Scrape success").

Package `internal/zabbixproto` : encodage/décodage pur, testable avec des
buffers/`net.Pipe()`, sans dépendance réseau réelle.

Package `internal/zabbixclient` : au-dessus, gère la connexion TCP, le
timeout, le parsing des cas spéciaux (`ZBX_NOTSUPPORTED`, JSON LLD),
expose `GetItem(ctx, addr, key) (string, error)` et
`Discover(ctx, addr, discoveryKey) ([]map[string]string, error)`.

## Découverte dynamique (LLD)

Pour les items multi-instances (points de montage, interfaces réseau, etc.),
on utilise les items de découverte natifs de l'agent Zabbix
(`vfs.fs.discovery`, `net.if.discovery`, ...) qui retournent un JSON listant
les entités avec leurs macros (`{#FSNAME}`, `{#IFNAME}`, ...). À chaque
scrape : on appelle la clé de discovery, on parse le JSON, puis pour chaque
entité on appelle la clé de valeur avec la macro substituée. Pas de liste
statique à maintenir manuellement — robuste aux changements (nouveau disque,
nouvelle interface).

## Configuration (Viper / YAML)

### Structure

```yaml
server:
  listen_addr: ":9290"
  log_level: "info"        # debug|info|warn|error
  log_encoding: "console"  # console|json

defaults:
  port: 10050
  timeout: 5s

hosts:
  srv-old-01: {}                      # pas d'override, hérite de defaults
  srv-old-02:
    port: 10150
    timeout: 10s

modules:
  linux:
    items:
      - key: "agent.ping"
        metric: "zabbix_agent_ping"
        type: gauge
        help: "Agent reachability (1=up)"
      - key: "system.cpu.load[,avg1]"
        metric: "zabbix_cpu_load1"
        type: gauge
        help: "CPU load average 1m"
      - key: "system.uptime"
        metric: "zabbix_uptime_seconds"
        type: gauge
        help: "System uptime in seconds"
      - key: "vm.memory.size[available]"
        metric: "zabbix_memory_available_bytes"
        type: gauge
        help: "Available memory in bytes"
      - key: "vm.memory.size[total]"
        metric: "zabbix_memory_total_bytes"
        type: gauge
        help: "Total memory in bytes"
    discovered_items:
      - discovery_key: "vfs.fs.discovery"
        value_key: "vfs.fs.size[{#FSNAME},pfree]"
        metric: "zabbix_fs_free_percent"
        type: gauge
        help: "Free space percent per filesystem"
        label_name: "mountpoint"
        label_field: "{#FSNAME}"
      - discovery_key: "net.if.discovery"
        value_key: "net.if.in[{#IFNAME}]"
        metric: "zabbix_net_if_in_bytes"
        type: counter
        help: "Network bytes received per interface"
        label_name: "interface"
        label_field: "{#IFNAME}"

groups:
  - name: "tous-linux"
    hosts: ["srv-old-01", "srv-old-02"]
    modules: ["linux"]
```

### Règles

- **`hosts`** : déclarés une seule fois, avec override optionnel de
  `port`/`timeout`. Un host non listé ici existe implicitement avec les
  valeurs `defaults`.
- **`groups`** : purement une agrégation de modules — référencent des noms
  de host (pas de duplication de définition). Un host peut appartenir à
  plusieurs groupes ; ses modules résolus par défaut sont **l'union** des
  modules de tous les groupes qui le contiennent. Les groupes ne portent pas
  d'override de port/timeout (seuls `defaults` et `hosts.<name>` en
  portent), pour éviter toute ambiguïté quand un host appartient à
  plusieurs groupes.
- **`modules.<name>.items`** : items simples (une clé → une série).
- **`modules.<name>.discovered_items`** : items liés à une LLD — une série
  par entité découverte, avec un label (`label_name`) dont la valeur vient
  de la macro substituée (`label_field`).

### Résolution à la requête

`GET /metrics?target=<host>[&module=<name>]*[&port=<p>][&timeout=<d>]`

1. **Modules** : si aucun `module` dans l'URL → union des modules de tous
   les groupes contenant `target`. Si un ou plusieurs `module` sont
   présents dans l'URL → **remplacent entièrement** la résolution par
   groupe pour ce scrape (comportement explicite, comme blackbox_exporter).
   Si aucun module n'est résolu (host inconnu des groupes et pas de
   `module` dans l'URL) → réponse HTTP 400 explicite.
2. **Port/timeout** : cascade `defaults → hosts.<target> → paramètres URL`.
   Chaque niveau ne redéfinit que ce qu'il change.

## Flux de scrape

1. Résoudre modules + port/timeout (cf ci-dessus).
2. Pour chaque module résolu, pour chaque item :
   - item simple : une requête `GetItem`.
   - item découvert : une requête `Discover` (discovery_key), puis une
     requête `GetItem` par entité retournée.
3. Toutes les requêtes vers un même host sont **séquentielles** (une
   connexion TCP à la fois — les agents Zabbix passifs ne gèrent qu'une
   requête par connexion). Plusieurs targets différents peuvent être
   scrapés en parallèle si Prometheus envoie des requêtes HTTP concurrentes
   (pas de verrou global côté exporter — le parallélisme est déjà borné par
   la config de scrape de Prometheus).
4. Gestion des erreurs par item : si un item échoue (timeout,
   `ZBX_NOTSUPPORTED`, erreur réseau), on logue en `warn` (zap, avec
   host/item/raison), on incrémente `zabbix_item_errors_total{target,item}`
   et on omet la série correspondante — le reste du scrape continue.
5. Métriques meta exposées en plus des métriques mappées :
   - `zabbix_scrape_success{target}` : **0 uniquement si l'agent est
     injoignable** dès le premier contact (connexion refusée/timeout sur le
     tout premier item interrogé) ; reste à **1** si l'agent répond même si
     certains items individuels échouent ensuite (dans ce cas, seul
     `zabbix_item_errors_total` augmente). Reflète la santé de la connexion,
     pas la complétude des données.
   - `zabbix_scrape_duration_seconds{target}`.
   - `zabbix_item_errors_total{target, item}`.
   - Métriques standard du process Go exposées via les collectors par
     défaut de `client_golang` (`promhttp`/`collectors`).

## Logging & configuration

- **zap**, niveau configurable (`server.log_level`), encodeur configurable
  (`server.log_encoding`: `console` en dev, `json` en prod).
- **Viper** pour charger `config.yaml`, avec **hot-reload** : `viper.WatchConfig()`
  (et/ou réaction à SIGHUP) recharge et revalide la config en mémoire sans
  redémarrer le process. Un rechargement invalide (YAML cassé, validation
  échouée) est rejeté avec un log `error` — la config précédente reste
  active.

## HTTP API

- `GET /metrics?target=<host>&module=<name>[&module=<name2>]&port=&timeout=`
  → scrape à la demande, retourne les métriques au format Prometheus.
- `GET /healthz` → 200 OK si le process et la config chargée sont sains.
- Rechargement : automatique via watch de fichier (+ SIGHUP), pas
  d'endpoint HTTP de reload nécessaire pour le MVP.

## Déploiement

Binaire Go statique unique + fichier de config YAML, lancé via systemd sur
un serveur Linux. Pas de conteneurisation nécessaire pour le MVP (pourra
être ajoutée ultérieurement sans changement d'architecture).

## Tests

- `internal/zabbixproto` : tests unitaires purs sur encodage/décodage,
  buffers en mémoire / `net.Pipe()`, sans réseau réel.
- `internal/zabbixclient` : tests avec un faux serveur TCP local
  (`net.Listen("tcp", "127.0.0.1:0")`) simulant un agent Zabbix (réponse
  normale, `ZBX_NOTSUPPORTED`, timeout, connexion refusée, JSON LLD).
- `internal/config` : tests de parsing YAML sur fixtures dans `testdata/`
  (cascade d'overrides, union de modules multi-groupes, remplacement de
  modules via URL).
- `internal/collector` : tests combinant le faux serveur Zabbix + une
  config fixture, vérification des métriques produites via
  `testutil.CollectAndCompare` (`client_golang`).
- `internal/httpserver` : tests de handlers via `httptest` (codes d'erreur :
  target manquant, module non résolu ; comportement du hot-reload).
- Pas de dépendance à un vrai agent Zabbix dans la suite automatisée —
  réservé à un test manuel/smoke ponctuel.

## Périmètre MVP

Un seul module d'exemple livré dans `configs/config.example.yaml` : `linux`,
couvrant `agent.ping`, charge CPU, uptime, mémoire disponible/totale, espace
disque libre par filesystem (via LLD `vfs.fs.discovery`) et trafic réseau
entrant par interface (via LLD `net.if.discovery`). Le mécanisme de modules
étant générique, l'ajout d'un module `windows` ou autre est une tâche de
configuration, pas de code.