# dbanondump

Copia tablas desde una base SQL (o desde un dump) a un **contenedor temporal en Docker**, anonimiza las
columnas que listes en un YAML y escribe un dump listo para desarrollo. El contenedor se destruye siempre
al terminar.

- Un binario estático en Go; lo único que necesitas en el equipo es Docker.
- Tres archivos YAML por proyecto (`config.yaml`, `columns.yaml`, `functions.yaml`) y un `.env` con la clave; el paquete no sabe nada de tu proyecto.
- Motores: **MySQL 8**, **MariaDB** y **PostgreSQL**. Se declara en el YAML (`engine:`).

> Usa una réplica o un usuario **de solo lectura** en el origen: la herramienta solo lee.
> El dump resultante está anonimizado pero sigue siendo **interno**: no lo publiques.

## Documentación

**[docs/USO.md](docs/USO.md)** es la guía de uso completa: descarga e instalación, referencia de cada archivo y
opción, `.env`, patrones, progreso en pantalla, auditoría y `check --suggest`, logs, solución de problemas y CI.
Los cambios de cada versión están en [CHANGELOG.md](CHANGELOG.md).

## Inicio rápido

Descarga el binario de la [última release](https://github.com/letkode/db-anon-dump/releases) (o compílalo
con `make build`), verifica su suma con `SHA256SUMS` y déjalo en tu `PATH`:

```bash
sha256sum -c --ignore-missing SHA256SUMS
install -m 0755 dbanondump-linux-amd64 ~/.local/bin/dbanondump
dbanondump version
```

Los binarios `linux-amd64` y `linux-arm64` se prueban de punta a punta en el CI (MySQL, MariaDB y PostgreSQL).
`darwin-arm64` solo se compila, arranca y valida un proyecto en un runner de macOS (sin Docker, así que no corre
contra una base de datos): **no está probado de punta a punta en macOS**.

```bash
dbanondump init dump/                              # config.yaml, columns.yaml, functions.yaml y .env de ejemplo
$EDITOR dump/.env                                  # DBANONDUMP_SOURCE_PASSWORD='...' (NUNCA en config.yaml)
dbanondump validate    -c dump/config.yaml         # lint offline: sin Docker ni base de datos
dbanondump check --suggest -c dump/config.yaml     # estructura real + auditoría + columns.suggested.yaml
dbanondump check       -c dump/config.yaml         # comprueba tus patrones contra la estructura real
dbanondump run         -c dump/config.yaml         # extrae, anonimiza y escribe el dump
```

Un proyecto, en una carpeta:

| Archivo | Qué contiene |
|---|---|
| `config.yaml` | motor, origen, base temporal, salida, tablas, logs, auditoría |
| `columns.yaml` | qué columnas se anonimizan y cómo (**solo** lo que listes; lo demás se copia tal cual) |
| `functions.yaml` | funciones SQL auxiliares para los patrones (opcional) |
| `.env` | la clave del origen (las capas `.env.local`, `.env.<APP_ENV>` también se leen) |

Un patrón es texto con SQL entre llaves: `"user-{id}@example.test"`, `"{NULL}"`, `"{KEEP}"` (revisada, se deja
con el dato real). Todo está explicado en la guía.

Tablas que deben existir pero sin filas: `tables.schema_only: [mail, app_request]` (o `--schema-only mail`);
`--schema-only '*'` escribe un dump solo de estructura. El archivo se llama `dev-dump-<fecha-hora>.sql.gz`
(`...-schema.sql.gz` si es solo estructura).

Qué hace `run`, en orden: levanta el contenedor, baja la estructura, **pre-vuelo** de los patrones contra la
estructura real, audita las columnas sin patrón, baja los datos, anonimiza en paralelo con verificación,
escribe el dump y destruye el contenedor. Mientras tanto muestra qué tabla está procesando, y cada ejecución
deja un log con errores y tiempos por fase.

**Un límite que debes conocer:** la herramienta **solo anonimiza lo que está en `columns.yaml`**. Córrelo con
`check` (y `--strict` en CI) cada vez que cambie el esquema.

## Desarrollo (todo en Docker, sin Go instalado)

```bash
make build         # binarios en dist/
make test          # tests unitarios
make vet
make tidy          # resolver dependencias (go.mod / go.sum)
make e2e-mysql     # prueba por motor: extrae, anonimiza y RESTAURA el dump en una base nueva; cubre
                   # comodín "*", functions.yaml, nombre con fecha y los modos de fallo
make e2e-mariadb
make e2e-postgres
make e2e-all
```

CI (`.github/workflows/`): `ci.yml` corre `vet`, `test` y los e2e de cada motor en cada PR. `release.yml`
compila los binarios y los publica con sus sumas SHA256 al subir un tag de versión sin prefijo `v` (por ejemplo `0.1.0`)
(`git tag 0.1.0 && git push origin 0.1.0`). No se publica imagen Docker: el programa necesita hablar con
Docker para levantar la base temporal, así que los binarios bastan.

```
cmd/dbanondump/      CLI (run, check, validate, init)
internal/config/     carga y valida los 3 YAML
internal/scaffold/   `init`: escribe los archivos de partida
internal/runlog/     un archivo de log por ejecución, con errores y tiempos
internal/suggest/    `check --suggest`: escribe columns.suggested.yaml (todo comentado)
internal/envfile/    carga las capas .env, .env.local, .env.<APP_ENV> al entorno del proceso
templates/           las plantillas *.dist y .env.dist (embebidas en el binario)
internal/pattern/    "texto {expr}" -> SQL
internal/engine/     interfaz Dialect + MySQL, MariaDB y PostgreSQL
internal/plan/       arma el UPDATE, el check y la auditoría por tabla
internal/tempdb/     ciclo de vida del contenedor y flujos de datos
internal/pipeline/   orquesta todo y define los códigos de salida
testdata/generic/    esquema, datos ficticios y config de cada motor, usados por los e2e
```

## Agregar otro motor

Implementa `engine.Dialect` (comillas, comparación con `NULL`, introspección, herramientas de dump, imagen del
contenedor) y regístralo en `engine.Get`. `internal/engine/postgres.go` es el ejemplo más completo.

## Roadmap

- **SQL Server y Oracle** no están incluidos. A diferencia de MySQL, MariaDB y PostgreSQL, no tienen una
  herramienta de dump que genere SQL (`mysqldump` / `pg_dump`): SQL Server necesita `bcp` o scripting externo y
  Oracle usa Data Pump (formato binario). Soportarlos exige un exportador propio en Go (DDL + `INSERT`s desde
  `information_schema` / el catálogo), imágenes pesadas (SQL Server solo amd64 y ≥ 2 GB de RAM; Oracle con
  arranque lento) y aceptar sus licencias. `engine: sqlserver` / `oracle` devuelve hoy un error explícito.
  Decisión: no se implementa por ahora (uso poco frecuente y costo alto; estimación sin medir: entre una y dos
  semanas y media para ambos). Si aparece una necesidad real, **empezar por el modo "en el lugar"** descrito
  abajo y no por el exportador propio.
- **Modo "en el lugar"** (idea, sin implementar): no extrae ni vuelca. Anonimiza una base que ya existe y es
  desechable: tú (o un DBA) restauras tu backup nativo (`.bak`, `.dmp`…) en una instancia aparte, la herramienta
  ejecuta el pre-vuelo, anonimiza y verifica, y el dump final lo haces con la herramienta nativa del motor. Solo
  requiere el dialecto (generación de SQL, catálogo y driver), sin exportador: unos 2 a 4 días por motor
  (estimación). También serviría para MySQL, MariaDB y PostgreSQL cuando ya tengas una copia desechable.
- Extracción en paralelo por tabla (hoy es un único dump consistente).

## Limitaciones

- `--from-dump` importa el dump completo (necesita disco para todo) y luego descarta lo no pedido.
- Para un origen en tu máquina usa `source.container_host: host.docker.internal`.
- Los textos libres y JSON que anonimices con `{NULL}` se pierden en el ambiente de desarrollo.
