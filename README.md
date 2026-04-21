# ragstore

Moteur de recherche documentaire headless pour agents LLM. Binaire Linux unique, sans dépendances, sans daemon — il s'indexe et se cherche en une seule commande.

## Pourquoi ragstore

Les agents LLM ont besoin d'accéder à une base documentaire sans gérer d'infrastructure. `ragstore` est conçu pour tourner dans le même container que l'agent : un seul binaire à appeler, un seul fichier d'index sur disque, une sortie JSON propre à consommer directement.

La recherche repose sur **BM25** (Okapi BM25), l'algorithme de ranking utilisé par Elasticsearch et Lucene. C'est le meilleur choix pour un environnement isolé : aucun appel réseau, aucun modèle d'embedding à charger, latence sub-milliseconde, et d'excellents résultats sur du vocabulaire technique et métier.

---

## Installation

```bash
# Télécharger le binaire (Linux x86_64)
curl -Lo ragstore https://your-host/ragstore
chmod +x ragstore

# Ou compiler depuis les sources (Go 1.22+)
go build -o ragstore -ldflags="-s -w" .
```

Le binaire est statique, auto-contenu, 2 Mo. Aucune dépendance système requise.

---

## Démarrage rapide

```bash
# Indexer un répertoire de documents
RAG_DB=/data/rag.db.json ./ragstore index /data/docs --chunk-size 250

# Chercher par topic
RAG_DB=/data/rag.db.json ./ragstore search "machine learning réseaux de neurones" --top 5
```

Sortie :

```json
{
  "ok": true,
  "data": [
    {
      "id": "211ccacbd4cf3c78",
      "path": "/data/docs/ml_intro.md",
      "title": "ml_intro.md",
      "chunk": 0,
      "score": 5.571,
      "snippet": "…L'apprentissage supervisé utilise des données labellisées pour entraîner des modèles de classification et de régression. L'apprentissage non supervisé découvre des patterns cachés…"
    }
  ]
}
```

---

## Référence des commandes

### `index <path> [--chunk-size N]`

Indexe un fichier ou un répertoire (récursif). Les fichiers déjà présents dans l'index sont ignorés — la commande est **idempotente**.

```bash
ragstore index /data/docs                    # chunk par défaut : 300 mots
ragstore index /data/docs --chunk-size 150   # chunks plus précis
ragstore index /data/docs /data/extra        # plusieurs chemins
```

Formats supportés : `.txt`, `.md`, `.rst`, `.org`, `.json`, `.yaml`, `.csv`, `.html`, `.py`, `.go`, `.js`, `.ts`, `.java`, `.c`, `.cpp`, `.rs`, `.sh`, et tout fichier texte. PDF supporté si `pdftotext` est installé (`apt install poppler-utils`).

Le chunking se fait par **paragraphes** jusqu'à atteindre la taille cible — les chunks respectent les coupures naturelles du texte.

| `--chunk-size` | Usage recommandé |
|---|---|
| 100–150 | Documents techniques denses, précision maximale |
| 200–300 | Q&A généraliste *(défaut recommandé)* |
| 400–500 | Contexte large, documents narratifs |

### `search <query> [--top N]`

Recherche dans l'index et retourne les chunks les plus pertinents, triés par score BM25 décroissant.

```bash
ragstore search "docker kubernetes orchestration" --top 5
ragstore search "politique de remboursement" --top 3
```

Le champ `snippet` contient l'extrait le plus pertinent du chunk, centré sur les termes de la requête. Il est directement utilisable pour formuler une réponse.

### `get <id>`

Retourne le contenu complet d'un chunk à partir de son identifiant (obtenu via `search`).

```bash
ragstore get 211ccacbd4cf3c78
```

Utile quand le snippet est tronqué et que davantage de contexte est nécessaire.

### `list`

Liste tous les documents indexés avec leur chemin, titre, numéro de chunk et nombre de mots.

```bash
ragstore list
```

### `delete <id|path>`

Supprime un ou plusieurs chunks par identifiant ou par préfixe de chemin.

```bash
ragstore delete 211ccacbd4cf3c78          # un chunk précis
ragstore delete /data/docs/ancien.md      # tous les chunks d'un fichier
ragstore delete /data/docs/archive/       # tous les chunks d'un répertoire
```

### `stats`

Affiche les statistiques de l'index.

```bash
ragstore stats
```

```json
{
  "ok": true,
  "data": {
    "total_chunks": 42,
    "unique_files": 12,
    "unique_terms": 3841,
    "avg_chunk_len": 187,
    "chunk_size_cfg": 250,
    "db": "/data/rag.db.json"
  }
}
```

### `interactive`

Lit des commandes depuis stdin, une par ligne. Pratique pour envoyer plusieurs requêtes en batch sans relancer le binaire.

```bash
echo "search apprentissage automatique --top 3" | ragstore interactive
```

---

## Configuration

| Variable d'environnement | Défaut | Description |
|---|---|---|
| `RAG_DB` | `./rag.db.json` | Chemin vers le fichier d'index |

L'index est un fichier JSON auto-suffisant. Plusieurs index peuvent coexister en pointant `RAG_DB` vers des chemins différents.

---

## Intégration dans un agent LLM

`ragstore` est conçu pour être référencé dans un **skill** d'agent. Le workflow type :

1. L'agent reçoit une question de l'utilisateur
2. Il extrait 3 à 6 mots-clés représentatifs du topic
3. Il appelle `ragstore search "<mots-clés>" --top 5`
4. Il lit les champs `snippet` des résultats pour formuler sa réponse
5. Si un snippet est insuffisant, il appelle `ragstore get <id>` pour le contenu complet

```
User: "Quelle est notre politique de congés ?"
Agent → ragstore search "politique congés jours RTT" --top 3
Agent → lit les snippets → formule la réponse
```

Le fichier `SKILL_ragstore.md` fourni dans ce dépôt décrit ce workflow en détail et peut être utilisé directement comme skill système.

### Intégration container

```dockerfile
COPY ragstore /usr/local/bin/ragstore
RUN chmod +x /usr/local/bin/ragstore
ENV RAG_DB=/data/rag.db.json

# Pré-indexer les documents à la construction de l'image
RUN ragstore index /data/docs --chunk-size 250
```

---

## Format de sortie

Toutes les commandes retournent du JSON sur **stdout**. Les erreurs vont sur **stderr** avec un code de sortie 1.

```json
// Succès
{ "ok": true, "message": "...", "data": { ... } }

// Erreur
{ "ok": false, "message": "description de l'erreur" }
```

---

## Compilation depuis les sources

```bash
# Prérequis : Go 1.22+
git clone https://github.com/your-org/ragstore
cd ragstore

# Build standard
go build -o ragstore .

# Build optimisé (taille réduite, pas de debug symbols)
go build -o ragstore -ldflags="-s -w" .

# Build statique (pour Alpine / containers minimaux)
CGO_ENABLED=0 GOOS=linux go build -o ragstore -ldflags="-s -w" .
```

Le code ne dépend que de la bibliothèque standard Go. Aucun module externe requis.

---

## Algorithme

**BM25 (Okapi BM25)** avec les paramètres classiques `k1=1.5`, `b=0.75`.

Le scoring d'un document `d` pour une requête `q` :

$$\text{score}(d, q) = \sum_{t \in q} \text{IDF}(t) \cdot \frac{f(t,d) \cdot (k_1 + 1)}{f(t,d) + k_1 \left(1 - b + b \cdot \frac{|d|}{\text{avgdl}}\right)}$$

Le tokenizer normalise en minuscules, supprime la ponctuation, et filtre une liste de stop-words multilingues. Il est agnostique à la langue et fonctionne aussi bien en français qu'en anglais ou en code source.

---

## Limites connues

- **Pas de recherche sémantique** : BM25 ne comprend pas les synonymes ou la paraphrase. Compenser en élargissant la requête (ex. `"voiture automobile véhicule"`). Un agent LLM peut reformuler automatiquement si les premiers résultats sont insuffisants.
- **PDF basique** : sans `pdftotext`, l'extraction PDF est limitée aux caractères ASCII imprimables. Installer `poppler-utils` pour une extraction correcte.
- **Pas de mise à jour incrémentale** : modifier un fichier déjà indexé ne met pas à jour ses chunks. Faire `delete <path>` puis `index <path>` pour réindexer.
- **Scalabilité** : l'index est chargé entièrement en mémoire. Adapté jusqu'à ~100k chunks (environ 500 Mo de texte brut). Au-delà, envisager Qdrant ou Elasticsearch.

---

## Licence

MIT
