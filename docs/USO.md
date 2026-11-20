# Guía de uso de dbanondump

Esta guía explica cómo descargar, configurar y usar `dbanondump` de principio a fin. El [README](../README.md)
es un resumen; aquí está el detalle.

## Índice

1. [Qué hace y cómo funciona](#1-qué-hace-y-cómo-funciona)
2. [Requisitos, descarga e instalación](#2-requisitos-descarga-e-instalación)
3. [Primeros pasos](#3-primeros-pasos)
4. [Los archivos del proyecto](#4-los-archivos-del-proyecto)
5. [`config.yaml`: referencia completa](#5-configyaml-referencia-completa)
6. [La clave y los archivos `.env`](#6-la-clave-y-los-archivos-env)
7. [`columns.yaml` y los patrones](#7-columnsyaml-y-los-patrones)
8. [`functions.yaml`](#8-functionsyaml)
9. [Comandos y opciones](#9-comandos-y-opciones)
10. [Qué se ve en pantalla: progreso y `--verbose`](#10-qué-se-ve-en-pantalla-progreso-y---verbose)
11. [Decidir qué anonimizar: auditoría y `check --suggest`](#11-decidir-qué-anonimizar-auditoría-y-check---suggest)
12. [Un `columns.yaml` vacío y `--allow-empty`](#12-un-columnsyaml-vacío-y---allow-empty)
13. [Tablas solo con estructura: `schema_only`](#13-tablas-solo-con-estructura-schema_only)
14. [Logs de ejecución](#14-logs-de-ejecución)
15. [Cargar el dump resultante](#15-cargar-el-dump-resultante)
16. [Códigos de salida](#16-códigos-de-salida)
17. [Notas por motor](#17-notas-por-motor)
18. [Solución de problemas](#18-solución-de-problemas)
19. [Seguridad y qué no subir a git](#19-seguridad-y-qué-no-subir-a-git)
20. [Uso en CI](#20-uso-en-ci)

---

## 1. Qué hace y cómo funciona

`dbanondump` copia tablas desde una base SQL (o desde un dump) a un **contenedor temporal en Docker**,
anonimiza las columnas que listes en un YAML y escribe un dump listo para desarrollo. El contenedor se
destruye siempre al terminar, incluso si algo falla.

Los pasos de un `run`, en orden:

1. Levanta el contenedor temporal con una clave aleatoria, publicado solo en `127.0.0.1`.
2. Resuelve `tables` y baja **solo la estructura** de esas tablas.
3. **Pre-vuelo** (segundos, antes de mover datos): comprueba cada patrón contra la estructura real
   (funciones inexistentes, columnas que no existen, errores de tipo, columnas `UNIQUE` sin `id`).
   Reporta todos los errores juntos (código 3).
4. **Auditoría**: lista las columnas que parecen personales y no tienen patrón. Con `--strict` es un error.
5. Baja los datos y verifica que ningún patrón produzca más caracteres de los que admite la columna.
6. Anonimiza las tablas **en paralelo**, cada una con su verificación.
7. Escribe el dump (sin funciones ni triggers; sin archivo parcial si algo falla) y destruye el contenedor.

> Usa una réplica o un usuario **de solo lectura** en el origen: la herramienta solo lee.
> El dump resultante está anonimizado pero sigue siendo **interno**: no lo publiques.

**Un límite que debes conocer:** la herramienta **solo anonimiza lo que está en `columns.yaml`**. Una columna
que no aparece se copia tal cual. Por eso existen `check`, `--strict` y `check --suggest`: córrelos cuando
cambie el esquema y revisa la lista antes de compartir un dump.

## 2. Requisitos, descarga e instalación

**Requisitos**

- **Docker** instalado y funcionando. Es lo único que necesitas; no hace falta Go.
- Una cuenta **de solo lectura** en la base de origen (idealmente en una réplica). En MySQL/MariaDB, `mysqldump`
  con `--single-transaction` puede pedir el privilegio global **`RELOAD`** (o `FLUSH_TABLES`) además de `SELECT`;
  si la cuenta no lo tiene, la extracción falla con `Access denied ... RELOAD or FLUSH_TABLES` (ver la sección 18). En MySQL/MariaDB, `mysqldump`
  con `--single-transaction` puede pedir el privilegio global **`RELOAD`** (o `FLUSH_TABLES`) además de `SELECT`;
  si no lo tiene la cuenta, la extracción falla con `Access denied ... RELOAD or FLUSH_TABLES` (ver la sección 18).
- Motores soportados: MySQL, MariaDB y PostgreSQL.

**Descarga.** Cada versión se publica en
`https://github.com/letkode/db-anon-dump/releases/tag/<versión>` (las versiones no llevan prefijo `v`).

| Sistema | Archivo |
|---|---|
| Linux x86-64 | `dbanondump-linux-amd64` |
| Linux ARM64 | `dbanondump-linux-arm64` |
| macOS Apple Silicon | `dbanondump-darwin-arm64` |

Con `gh` (también sirve si el repositorio es privado, siempre que tu cuenta tenga acceso):

```bash
gh release download <versión> --repo letkode/db-anon-dump -p dbanondump-linux-amd64 -p SHA256SUMS
```

O con `curl` (solo si el repositorio es público):

```bash
curl -LO https://github.com/letkode/db-anon-dump/releases/download/<versión>/dbanondump-linux-amd64
curl -LO https://github.com/letkode/db-anon-dump/releases/download/<versión>/SHA256SUMS
```

**Verificar e instalar**

```bash
sha256sum -c --ignore-missing SHA256SUMS        # en macOS: shasum -a 256 -c --ignore-missing SHA256SUMS
chmod +x dbanondump-linux-amd64
mkdir -p ~/.local/bin
install -m 0755 dbanondump-linux-amd64 ~/.local/bin/dbanondump
dbanondump version                              # dbanondump <versión>
```

En macOS, si el sistema bloquea el binario: `xattr -d com.apple.quarantine dbanondump-darwin-arm64`.

**Qué está probado en cada binario.** `linux-amd64` y `linux-arm64` salen del mismo código y el CI los ejecuta de
punta a punta (MySQL, MariaDB y PostgreSQL, con restauración del dump). `darwin-arm64` solo se prueba en un runner
de macOS, que no tiene Docker: se compilan y pasan los tests unitarios, y el binario arranca (`version`) y valida un
proyecto recién generado (`init` + `validate`). **No se ha ejecutado contra una base de datos en macOS**: si lo usas
ahí, haz primero un `check` pequeño. Un binario de otra arquitectura falla de inmediato con `Exec format error`. La imagen
del contenedor temporal (`temp.image`) debe existir para tu arquitectura: las oficiales de MySQL 8.x, MariaDB y
PostgreSQL la tienen para arm64 y amd64, pero versiones antiguas (por ejemplo MySQL 5.7) no tienen arm64.

**Compilar desde el código** (solo si lo necesitas): `make build` deja los tres binarios en `dist/`. Solo
vuelves a compilar si cambia el código; cambiar los YAML nunca lo requiere.

## 3. Primeros pasos

```bash
dbanondump init dump/                  # crea config.yaml, columns.yaml, functions.yaml y .env
$EDITOR dump/config.yaml               # motor, host, usuario (solo lectura), base, imagen temporal
$EDITOR dump/.env                      # la clave: DBANONDUMP_SOURCE_PASSWORD='...'
dbanondump validate -c dump/config.yaml           # lint offline: sin Docker ni base de datos
dbanondump check --suggest -c dump/config.yaml    # estructura real + auditoría + columns.suggested.yaml
$EDITOR dump/columns.suggested.yaml    # decide, descomenta y renombra a columns.yaml
dbanondump check -c dump/config.yaml   # comprueba los patrones contra la estructura real
dbanondump run   -c dump/config.yaml   # extrae, anonimiza y escribe el dump
```

`init` se niega a tocar nada si alguno de los cuatro archivos ya existe (no deja archivos a medias).
`--force` los sobrescribe todos. Las plantillas van embebidas en el binario, así que `init` funciona en
cualquier servidor sin copiar nada más.

## 4. Los archivos del proyecto

Un proyecto es una carpeta con estos archivos. Se le pasa solo `config.yaml` (`-c`), que apunta a los demás:

| Archivo | Qué contiene | Cuándo cambia |
|---|---|---|
| `config.yaml` | motor, origen, base temporal, salida, tablas, logs, auditoría | por ambiente |
| `columns.yaml` | qué columnas se anonimizan y cómo | con el esquema (va en el PR de cada migración) |
| `functions.yaml` | funciones SQL auxiliares que los patrones pueden llamar | casi nunca |
| `.env` (+ `.env.local`, `.env.<APP_ENV>`...) | la clave del origen | por persona o ambiente; **nunca va a git** |

Las rutas relativas se resuelven contra la carpeta de `config.yaml`. Los archivos que genera el programa
(`output/`, `logs/`, `columns.suggested.yaml`) también quedan ahí.

## 5. `config.yaml`: referencia completa

```yaml
version: 1
engine: mysql                 # mysql | mariadb | postgres. Un proyecto = un motor

source:
  host: replica.internal
  port: 3306                  # por defecto 3306 (5432 en postgres)
  user: readonly
  database: myapp
  # password_env: DBANONDUMP_SOURCE_PASSWORD   # nombre de la variable con la clave
  # container_host: host.docker.internal       # host del origen visto desde el contenedor
  # dump_file: ./prod.sql.gz                   # alternativa: importar un dump en vez de conectarse

temp:
  image: mysql:8.4            # misma versión mayor que el origen (por defecto, la del motor)
  # database: anon_tmp
  # tmpfs: true               # datadir en RAM: más rápido, usa memoria
  # extra_args: ["--innodb-buffer-pool-size=2G"]

output: output/dev-dump-{datetime}.sql.gz   # {datetime} = 20261119-153045 · {date} = 2026-11-19

tables:
  include: ["*"]              # nombre, glob (user_*) o * para todas
  exclude: [mail, audit_*]    # las exclusiones siempre ganan; no salen en el dump
  # schema_only: [app_request]  # salen con su estructura pero SIN filas (sección 13)

logs:                         # opcional
  dir: logs                   # "" = no guardar logs (por defecto: logs)
  keep: 30                    # conserva los N más nuevos (por defecto 0 = todos)

audit:                        # opcional: reemplaza por completo las listas internas (ver sección 11)
  names: [mail, phone, rut, name]
  types: [json, text, longtext]

columns: columns.yaml         # por defecto, columns.yaml junto a este archivo (debe existir)
functions: functions.yaml     # por defecto, functions.yaml junto a este archivo (si existe)
```

| Clave | Descripción |
|---|---|
| `engine` | `mysql`, `mariadb` o `postgres`. Los patrones se escriben en el SQL de ese motor. |
| `source.*` | Dónde se leen los datos. Usa una cuenta de solo lectura. Con `dump_file` no se necesita conexión ni clave. |
| `source.container_host` | Si el origen corre en **esta misma máquina**, pon `host.docker.internal`: el contenedor no ve `localhost`. |
| `temp.image` | Imagen de la base temporal. Misma versión mayor que el origen. |
| `temp.tmpfs` | `true` lleva los datos a RAM: más rápido, usa memoria. |
| `output` | Archivo del dump. Un sufijo `.gz` lo comprime. `{datetime}` (20261119-153045) y `{date}` (2026-11-19) se expanden; por defecto lleva `{datetime}` para que dos ejecuciones del mismo día no se pisen. También se puede cambiar con `-o`. |
| `tables` | Qué tablas extraer (`include`, `exclude`) y cuáles van solo con estructura (`schema_only`, sección 13). Las exclusiones siempre ganan. |
| `logs.dir` / `logs.keep` | Carpeta y retención del log de cada ejecución (sección 13). |
| `audit.names` / `audit.types` | Qué columnas se consideran "personales" (sección 11). |

Una clave desconocida en `config.yaml` es un error: un typo no se ignora.

## 6. La clave y los archivos `.env`

La clave del origen **no** va en `config.yaml`. `dbanondump` la lee de archivos `.env` en la misma carpeta
que `config.yaml` (`init` crea el `.env` a partir de `templates/.env.dist`):

```
DBANONDUMP_SOURCE_PASSWORD='tu-clave'
```

Se leen en este orden y el último que defina una variable gana:

| Archivo | Para qué |
|---|---|
| `.env` | valores base |
| `.env.local` | tus cambios locales, sobre `.env` |
| `.env.<APP_ENV>` | valores de un ambiente (`.env.dev`, `.env.staging`...) |
| `.env.<APP_ENV>.local` | tus cambios locales de ese ambiente |

- `APP_ENV` se toma del entorno real o, si no está, de `.env` / `.env.local`. Sin `APP_ENV` solo se leen
  `.env` y `.env.local`.
- Una variable ya exportada en el entorno **gana siempre** sobre todos los archivos (útil en CI: se exporta y
  no hacen falta archivos).
- Los archivos que no existen se ignoran. Un archivo mal formado es un error.
- El nombre de la variable se cambia con `source.password_env`.
- Formato `CLAVE=valor`, comentarios `#`, comillas y `export` (lo interpreta
  [`joho/godotenv`](https://github.com/joho/godotenv)).
  **Usa comillas simples** si la clave tiene `$`: sin comillas o con comillas dobles, `$` inicia una
  expansión de variable.
- Protege el archivo: `chmod 600 .env` (`init` ya lo crea así).

## 7. `columns.yaml` y los patrones

Solo lo que se anonimiza; todo lo demás se copia tal cual.

```yaml
"*":                          # comodín: toda tabla extraída que tenga esa columna
  ip_address: "192.0.2.{(id % 254) + 1}"
user:
  email: "user-{id}@example.test"
  birthdate: "{MAKEDATE(YEAR(birthdate), 1)}"
  notes: "{NULL}"
  country: "{KEEP}"           # revisada: se queda como está
  plain_password:
    pattern: "{NULL}"
    optional: true            # no falla si la columna no existe en ese ambiente
```

Un patrón es texto con **expresiones SQL entre llaves**. Lo de afuera es literal; lo de adentro se copia
tal cual y debe ser SQL válido **del motor declarado**.

| Patrón | Resultado |
|---|---|
| `user-{id}@example.test` | `user-15@example.test` |
| `+56900{id}` | `+5690015` |
| `{MAKEDATE(YEAR(birthdate), 1)}` | 1 de enero del mismo año (MySQL / MariaDB) |
| `{make_date(EXTRACT(YEAR FROM birthdate)::int, 1, 1)}` | lo mismo en PostgreSQL |
| `{NULL}` | `NULL`; si la columna es `NOT NULL`, usa `''`, `0`, `'{}'`, `false`, `1970-01-01`… según el tipo |
| `{KEEP}` | revisada: se deja **tal cual, con el dato real**, y silencia la auditoría |
| `{REGEXP_REPLACE(...)}` | enmascara correos y teléfonos **dentro** de un texto libre y conserva el resto; ver el ejemplo en la plantilla |

Reglas que se aplican solas:

- **Conserva los `NULL`**: si el valor original es `NULL`, sigue `NULL`.
- **Determinista**: usa `{id}` (u otra columna como `{user_id}`) para que el mismo dato en dos tablas
  coincida y los JOIN sigan funcionando. Refiérete solo a columnas **no anonimizadas** o a la propia columna.
- **Columnas `UNIQUE` de texto**: se "aparcan" primero (`t-<id>`) para que el valor final no choque con uno
  real. Requiere una columna `id`; si falta, el pre-vuelo lo informa.
- **Verificación**: después de cada tabla se cuentan las filas que no coinciden con su patrón; debe dar `0`.
- **El patrón debe poder repetirse**: la verificación vuelve a evaluarlo sobre la fila ya cambiada, así que tiene
  que dar el mismo valor la segunda vez. Puede usar `{id}` u otras columnas que no anonimizas, y su propia
  columna solo si es **idempotente** (`MAKEDATE(YEAR(birthdate), 1)` sí; `MD5(clave)` **no**, porque el hash de un
  hash es otro valor y la verificación falla con el código 6). Para un hash usa `{MD5(CONCAT(id, ''))}` (el `CONCAT` vuelve texto el `id`: `md5()` de PostgreSQL no acepta enteros).
- Una entrada exacta (`user.email`) gana sobre el comodín (`"*".email`).
- Una entrada para una tabla que **no** extraes se ignora; una columna que no existe en una tabla que sí
  extraes es un error (un typo dejaría el dato sin tratar), salvo `optional: true`.

> **Cuidado con `{KEEP}`.** Significa "revisé esta columna y es segura". El dump llevará el valor real.
> Úsalo solo en columnas que realmente revisaste.

## 8. `functions.yaml`

Funciones SQL que se crean en la base **temporal** (no van en el dump) para que los patrones las llamen.
Cada `sql` es **una sola sentencia**: no hace falta `DELIMITER`, y el programa hace
`DROP FUNCTION IF EXISTS <name>` antes de crearla.

```yaml
- name: fake_rut
  sql: |
    CREATE FUNCTION fake_rut(p_rut BIGINT) RETURNS VARCHAR(12) DETERMINISTIC NO SQL
    BEGIN
        RETURN CONCAT(p_rut, '-', fake_rut_dv(p_rut));
    END
```

El archivo es opcional: si tus patrones no llaman funciones propias, déjalo vacío (`[]`). En PostgreSQL el
`DROP FUNCTION IF EXISTS <name>` falla si el nombre tiene varias sobrecargas; usa nombres únicos.

## 9. Comandos y opciones

| Comando | Qué hace |
|---|---|
| `init [dir]` | Escribe `config.yaml`, `columns.yaml`, `functions.yaml` y `.env` de ejemplo (`--force` sobrescribe). |
| `validate` | Lint offline de los archivos. Sin Docker ni base de datos. |
| `check` | Valida contra la **estructura** real y audita las columnas sin patrón. No carga datos ni escribe el dump. |
| `run` | Extrae, anonimiza, verifica y escribe el dump. |
| `version` | Muestra la versión. |

**Opciones comunes**

| Opción | Efecto |
|---|---|
| `-c, --config FILE` | `config.yaml` (por defecto `./config.yaml`). |

**`run` y `check`**

| Opción | Efecto |
|---|---|
| `-t, --tables LIST` | Tablas separadas por comas; reemplaza `tables.include`. `!nombre` excluye. |
| `-x, --exclude LIST` | Tablas o globs a excluir (se suman a la config). |
| `--from-dump FILE` | Importa este dump (`.sql` / `.sql.gz`) en vez de leer el origen. |
| `--schema-only LIST` | Tablas (nombres, globs, `*`) que van con estructura pero sin filas; se suma a `tables.schema_only`. Con `'*'` el dump es solo de estructura (sección 13). |
| `--strict` | Sale con código 3 si una columna "personal" no tiene patrón. |
| `--log-dir DIR` | Carpeta del log de esta ejecución. |
| `--no-log` | No escribe log. |
| `--verbose` | Más detalle (contenedor, tablas, tiempos) y los mensajes del driver de la base. |

**Solo `check`**

| Opción | Efecto |
|---|---|
| `--suggest` | Escribe `columns.suggested.yaml` con cada columna marcada y una sugerencia comentada. |

**Solo `run`**

| Opción | Efecto |
|---|---|
| `-o, --output FILE` | Archivo del dump; `.gz` comprime; `{datetime}` y `{date}` se expanden. |
| `--parallel N` | Tablas anonimizadas a la vez (por defecto `min(4, CPUs)`). |
| `--keep-container` | Conserva el contenedor temporal. **Tiene datos reales** hasta que lo borres: `docker rm -f -v <nombre>`. |
| `--allow-empty` | Permite un `columns.yaml` sin reglas (sección 12). |

Ejemplos:

```bash
dbanondump run -t user,candidate                  # solo estas tablas
dbanondump run -t '*' -x mail,job_offer           # todas menos estas
dbanondump run --from-dump prod.sql.gz            # desde un dump en vez del origen
dbanondump run -o /tmp/dev.sql.gz --parallel 8
dbanondump check --strict                         # código 3 si hay columnas personales sin patrón
```

## 10. Qué se ve en pantalla: progreso y `--verbose`

Los pasos largos dicen qué están haciendo, para distinguir "trabajando" de "atascado". Todo lo que sale por
pantalla también queda en el [log](#13-logs-de-ejecución).

```
[dbanondump] starting temporary mysql (mysql:8.4)
[dbanondump] reading the structure of 114 table(s) from replica.internal:3306/myapp (read-only)
[dbanondump]   structure [1/114] access_token
[dbanondump]   structure [2/114] additional_module
[dbanondump]   structure: still on user_info_personal (table 37/114): 40s in this table, 12.3 MB received, 2m10s elapsed
[dbanondump] tables to extract: 114
[dbanondump] pre-flight: checking 12 pattern(s) against the real structure
[dbanondump] extracting the data of 114 table(s)
[dbanondump]   data [1/114 ~0%] access_token
[dbanondump]   data [2/114 ~3%] additional_module
[dbanondump] anonymising 12 table(s) (4 in parallel)
[dbanondump]   anonymising: 3/12 done, working on: user (12s), person (8s)
[dbanondump]   ok  [4/12] user (14.2s)
[dbanondump] writing the dump: output/dev-dump-20261119-153045.sql.gz
[dbanondump]   dump: 120.0 MB so far, 20s elapsed
```

- **Una línea por tabla** al copiar la estructura y los datos: `[n/total] tabla`. En los datos se añade un
  porcentaje **aproximado** (`~41%`), calculado con el tamaño que la base informa por tabla.
- **Latido cada 10 segundos** (`still on ...`): si una tabla tarda, dice cuál es, cuánto lleva en ella y
  cuántos bytes han pasado. Con eso se ve si lo lento es el origen o el destino.
- **Anonimización**: cada tabla se imprime al terminar, con su duración y `[n/total]`; el latido indica qué
  tablas están en curso y desde hace cuánto.
- **Importar un dump** (`--from-dump`) y **escribir el dump final** muestran los bytes procesados.

**`--verbose`** agrega: el nombre del contenedor, la lista de tablas, cuánto tardó la base temporal en estar
lista, y los mensajes internos del driver de MySQL (por ejemplo `unexpected EOF`, que aparecen mientras el
contenedor arranca y **no son errores**). Sin `--verbose` esos mensajes se ocultan.

## 11. Decidir qué anonimizar: auditoría y `check --suggest`

### Auditoría

`check` y `run` listan las columnas que **parecen personales** y no tienen patrón en `columns.yaml`. Una
columna "parece personal" si:

- su **nombre** coincide con alguna expresión de `audit.names`, o
- su **tipo** está en `audit.types` (texto largo y JSON, porque pueden contener cualquier cosa).

Sin sección `audit:` se usan listas internas (`mail`, `phone`, `rut`, `name`, `password`, `token`... y los
tipos `json`, `text`, `longtext`...). **Si la sección existe, reemplaza esas listas por completo**: solo se
audita lo que tú escribas. Así funciona también en bases con nombres en otro idioma (`correo`, `telefono`,
`direccion`...). `dbanondump init` ya trae la lista completa para editarla.

```yaml
audit:
  names:                      # expresiones regulares, sin distinguir mayúsculas, sobre el nombre de la columna
    - ^correo$
    - telefono
    - rut|dni
    - ^ip$
  types:                      # tipos tal como los informa la base
    - json
    - longtext
```

- Si pones `audit:` con solo `names`, no se audita por tipo (y al revés).
- Los nombres de columna pueden llevar espacios o acentos (`"Apellido Paterno"`): en `columns.yaml` van entre
  comillas como clave, y en el patrón se citan con el identificador del motor (comillas invertidas en MySQL,
  comillas dobles en PostgreSQL). `check --suggest` ya lo hace por ti.
- Los nombres de columna pueden llevar espacios o acentos (`"Apellido Paterno"`): en `columns.yaml` van entre
  comillas como clave, y en el patrón se citan con el identificador del motor (`` `Apellido Paterno` `` en MySQL,
  `"Apellido Paterno"` en PostgreSQL). `check --suggest` ya lo hace por ti.
- La auditoría es una **ayuda, no una lista definitiva**: no detecta columnas cuyo nombre no coincide (por
  ejemplo `plate`, `image`). Revisa el esquema además de la lista.
- `{KEEP}` saca una columna de la lista (la marcas como revisada).

### `check --suggest`

Con muchas columnas marcadas, escribirlas a mano es tedioso. `check --suggest` genera
**`columns.suggested.yaml`** junto a `config.yaml`:

```yaml
## columns.suggested.yaml -- written by `dbanondump check --suggest`.
## EVERY line below is commented out: nothing is anonymised until you decide.
...
# user:
#   email: "user-{id}@example.test"   ## varchar(255)
#   phone: "+56900{(id % 1000000) + 1000000}"   ## varchar(15): fake mobile number
#   second_phone: "{LEFT(MD5(CONCAT(id, '')), 8)}"   ## varchar(8): column too short for a readable value, so a hash prefix
#   password: "{MD5(CONCAT(id, ''))}"   ## varchar(255): irreversible and unique (built from the id, so the check can repeat it)
#   birthdate: "{MAKEDATE(YEAR(birthdate), 1)}"   ## date: keeps the year, hides the day
#   notes: "{NULL}"   ## text: decide. {NULL} drops the content, {KEEP} leaves it, or mask only e-mails/phones inside it
```

- **Todo viene comentado**: nada se anonimiza ni se aprueba solo. No toca tu `columns.yaml`.
- Las sugerencias salen del **nombre y el tipo** de la columna: correo, teléfono, documento de identidad,
  nombre, apellido, dirección, fecha de nacimiento, IP, URL, y secretos (hash irreversible). Respetan el
  largo de la columna; una columna corta recibe un prefijo de hash.
- Texto libre, JSON, columnas que no son texto y nombres ambiguos (`name`) llevan la nota **`decide`**: no hay
  una sugerencia automática segura.
- Una tabla sin columna `id` no recibe patrones basados en `id`.
- Las columnas de tablas `schema_only` no se listan ni se sugieren (no tienen datos).
- Las columnas de tablas `schema_only` no se listan ni se sugieren (no tienen datos).
- Si el archivo ya existía, se sobrescribe: **renómbralo o cópialo antes de volver a ejecutarlo**.

**Flujo recomendado**

1. `dbanondump check --suggest -c config.yaml`
2. Abre `columns.suggested.yaml`: descomenta (quita el `# ` inicial) lo que quieras y ajusta los patrones.
   Las líneas que empiezan con `##` son notas.
3. Renómbralo a `columns.yaml`, o copia las líneas a tu `columns.yaml`.
4. `dbanondump check -c config.yaml`: comprueba que los patrones corren en tu motor. Repite hasta que la lista
   de columnas sin patrón sea la que esperas (o vacía con `--strict`).
5. `dbanondump run -c config.yaml`.

## 12. Un `columns.yaml` vacío y `--allow-empty`

Un `columns.yaml` sin reglas es válido, por ejemplo mientras decides qué anonimizar:

- `validate` y `check` lo aceptan y muestran `WARNING: columns.yaml has no column to anonymise`.
- `run` **se niega** (código 3), porque el dump contendría los datos reales tal cual. Si es lo que quieres,
  pásale `--allow-empty` (una copia fiel de tu base con el mismo flujo, sin anonimizar).
- Excepción: si todas las tablas son `schema_only` (`--schema-only '*'`) no hay datos que filtrar y `run` no lo pide
  (sección 13).

## 13. Tablas solo con estructura: `schema_only`

A veces una tabla debe **existir** en la base de desarrollo, pero **sin sus filas** (por ejemplo `mail`,
`app_request` o una cola de trabajos). Con `exclude` la tabla desaparece del dump y la aplicación falla al usarla;
con `schema_only` aparece con su `CREATE TABLE` y vacía.

```yaml
tables:
  include: ["*"]
  exclude: [audit_*]                                  # NO están en el dump (ni la estructura)
  schema_only: [mail, app_request, upload_file_log]   # están, pero vacías
```

Acepta nombres, globs (`mail_*`) y `*` (todas), igual que `include` y `exclude`. También por línea de comandos:

```bash
dbanondump run --schema-only mail,app_request    # se SUMA a lo que diga la config
dbanondump run --schema-only '*'                 # un dump solo de estructura (entre comillas: el shell expande el *)
```

| Configuración | Resultado |
|---|---|
| `schema_only` ausente o `[]` | todas las tablas llevan datos (lo normal) |
| `schema_only: [mail, audit_*]` | esas quedan vacías; las demás llevan datos |
| `schema_only: ["*"]` o `--schema-only '*'` | **todas** solo con estructura |

**Quién manda**

- `include` / `exclude` deciden **qué tablas existen** en el dump, y `exclude` siempre gana: una tabla excluida no
  sale ni con estructura, aunque esté en `schema_only`.
- `schema_only` decide si las tablas que existen llevan **datos**.
- `--schema-only` se **suma** a la lista de la config (igual que `-x` se suma a `exclude`), así que un error de tipeo
  nunca saca más datos de los esperados.

**Qué pasa con las tablas `schema_only`**

- No se bajan sus datos (con `--from-dump`, el dump se importa completo y esas tablas se vacían después).
- No se anonimizan, no se auditan y no aparecen en `check --suggest`: sin datos no hay nada que proteger. Si
  `columns.yaml` tiene patrones para una de ellas, se avisa de que no tendrán efecto (el pre-vuelo sí los valida).
- El progreso de los datos se calcula solo con las tablas que llevan datos.

**Un dump solo de estructura** (cuando `schema_only` cubre todas las tablas elegidas):

- El archivo lleva `-schema` en el nombre (`output/dev-dump-20261119-153045-schema.sql.gz`) para que nunca pise un
  dump con datos. Con `-o` se usa exactamente el nombre que pongas.
- `columns.yaml` puede estar vacío y `run` no pide `--allow-empty`, porque no hay datos que filtrar.

## 14. Logs de ejecución

Cada `run` y `check` escribe `logs/<comando>-<fecha-hora>.log` junto a `config.yaml` (por ejemplo
`logs/run-20261119-153045.log`), con permisos 600:

- las mismas líneas que salen por pantalla, cada una con su hora;
- el error, si falla, con el código de salida;
- un resumen final con el **resultado**, la **duración total** y el **tiempo de cada fase** (base temporal,
  estructura, pre-flight, extracción de datos, anonimización, dump y limpieza), también si la ejecución falla.

```
---- summary ----
command:  run
result:   OK (exit 0)
started:  2026-11-19 15:30:45
duration: 3m12.4s
phases:
  temporary database     12.3s
  structure              4.1s
  ...
```

- `logs.dir` / `logs.keep` en `config.yaml`, o `--log-dir DIR` / `--no-log` por ejecución.
- `validate` e `init` no guardan log.
- La clave del origen nunca se escribe en el log (se reemplaza por `***`), pero los errores de SQL pueden citar
  fragmentos de datos reales: por eso el archivo es 600 y `logs/` va en `.gitignore`.
- Los errores anteriores a leer `config.yaml` (archivo inexistente o inválido) solo salen por pantalla.
- Si no se puede escribir el log (permisos, disco lleno) se avisa y la ejecución continúa.

## 15. Cargar el dump resultante

```bash
gunzip -c output/dev-dump-20261119-153045.sql.gz | docker exec -i <mysql-container> mysql -uroot -p<password> <database>
gunzip -c output/dev-dump-20261119-153045.sql.gz | docker exec -i <pg-container> psql -U postgres -d <database>
```

El dump no incluye funciones ni triggers. Está anonimizado pero sigue siendo **interno**.

## 16. Códigos de salida

| Código | Significado |
|---|---|
| `0` | OK |
| `2` | Uso incorrecto o archivo inválido (incluye clave que falta) |
| `3` | Problema de config o pre-vuelo, `--strict`, o `columns.yaml` vacío en `run` sin `--allow-empty` |
| `5` | Falló una sentencia de anonimización |
| `6` | Falló una verificación (¿un patrón no determinista?) |
| `7` | Falló la extracción o la importación |

## 17. Notas por motor

| Motor | Imagen por defecto | Herramientas | Notas |
|---|---|---|---|
| `mysql` | `mysql:8.0.32` | `mysqldump`, `mysql` | |
| `mariadb` | `mariadb:10.11` | `mariadb-dump`, `mariadb` | Las columnas JSON son `LONGTEXT` con un `CHECK`: si una es `NOT NULL`, escribe un patrón válido explícito, por ejemplo `"{'{}'}"`. |
| `postgres` | `postgres:16` | `pg_dump`, `psql` | Esquema `public`. `pg_dump -t` no sigue dependencias: si una tabla extraída tiene una FK a una tabla **no** extraída, no se puede restaurar. Extrae también la referenciada. Los patrones usan SQL de PostgreSQL (`MOD(id, 25)`, `make_date`...). |

## 18. Solución de problemas

| Síntoma | Qué pasa y qué hacer |
|---|---|
| `validate`: `columns.yaml: ...` | Mira el mensaje: nombra el archivo y la columna con el problema. |
| `check`/`run` tarda minutos en la fase **structure** | Es la copia del esquema desde el origen al contenedor. El latido muestra en qué tabla está. Prueba `temp.tmpfs: true` y revisa la latencia hacia el origen; con `--verbose` ves cuánto tarda cada paso. |
| Líneas `[mysql] ... unexpected EOF` | Ruido del driver mientras el contenedor arranca; no son errores. Ya se ocultan; con `--verbose` se muestran. |
| `the source password must be in the DBANONDUMP_SOURCE_PASSWORD environment variable` | Falta la clave: ponla en `.env` junto a `config.yaml` o expórtala. Revisa `source.password_env`. |
| No conecta al origen que corre en tu máquina | Usa `source.container_host: host.docker.internal`. |
| `run` termina con código 3 por `columns.yaml` vacío | Es la protección de la sección 12. Agrega reglas, usa `--allow-empty`, o `--schema-only '*'` si solo quieres la estructura. |
| `the column does not exist in the database` | Typo, o la columna solo existe en otro ambiente: márcala `optional: true`. |
| `the pattern ... produces N characters but the column holds M` | El patrón es más largo que la columna: acórtalo o usa un prefijo (`LEFT(...)`). |
| `check failed ... do not match their pattern` (código 6) | Un patrón no es determinista (por ejemplo usa `RAND()`); hazlo depender de `{id}`. |
| `mysqldump: Couldn't execute 'FLUSH TABLES': Access denied ... RELOAD or FLUSH_TABLES` | La cuenta del origen no tiene un privilegio **global**: tener `ALL` sobre la base no basta. Dale `RELOAD` (o `FLUSH_TABLES`): `GRANT RELOAD ON *.* TO 'usuario'@'%';`, o usa otra cuenta para extraer. |
| `WARNING: schema_only "x" matches no extracted table` | Ese nombre o glob no coincide con ninguna tabla extraída: un typo, o la tabla está excluida o fuera de `include`. Revisa los nombres reales de las tablas. |
| La fase **structure** es lenta aunque el origen responde rápido | Casi todo el tiempo lo gasta el MySQL temporal aplicando los `CREATE TABLE`. Prueba `temp.tmpfs: true` (en una base de 262 tablas bajó esa fase de ~10 s a ~3 s). |
| `mysqldump: Couldn't execute 'FLUSH TABLES': Access denied ... RELOAD or FLUSH_TABLES` | La cuenta del origen no tiene un privilegio **global**: tener `ALL` sobre la base no basta. Dale `RELOAD` (o `FLUSH_TABLES`), por ejemplo `GRANT RELOAD ON *.* TO 'usuario'@'%';`, o usa otra cuenta para extraer. |
| `WARNING: schema_only "x" matches no extracted table` | Ese nombre o glob no coincide con ninguna tabla extraída: un typo, o la tabla está excluida o fuera de `include`. Revisa los nombres reales. |
| La fase **structure** es lenta aunque el origen responde rápido | Casi todo el tiempo lo gasta el MySQL temporal aplicando los `CREATE TABLE`. Prueba `temp.tmpfs: true` (en una base de 262 tablas esa fase bajó de ~10 s a ~3 s). |
| Quedó un contenedor `dbanondump-...` activo | Si usaste `--keep-container` o cortaste el proceso a la fuerza: `docker rm -f -v <nombre>`. |

El log (`logs/`) tiene el detalle de cada ejecución, incluidos los tiempos por fase.

## 19. Seguridad y qué no subir a git

- Usa una cuenta **de solo lectura**, idealmente en una réplica.
- El contenedor temporal usa una clave aleatoria y solo escucha en `127.0.0.1`.
- **No subas a git**: `.env`, `.env.local` y cualquier `.env.<algo>` (el repositorio ya los ignora, salvo
  `.env.dist`), `logs/`, `output/` y `dist/`. Si tu carpeta de proyecto vive dentro de **otro** repositorio,
  agrégalos también a su `.gitignore`.
- El dump está anonimizado pero es **interno**. Antes de compartirlo, revisa la lista de `check`.
- `--keep-container` deja datos reales en un contenedor hasta que lo borres.
- Los textos libres y JSON que anonimices con `{NULL}` se pierden en el ambiente de desarrollo.

## 20. Uso en CI

En CI no hacen falta archivos `.env`: exporta la clave como secreto.

```bash
export DBANONDUMP_SOURCE_PASSWORD="$SECRET"
dbanondump validate -c dump/config.yaml
dbanondump check --strict -c dump/config.yaml     # falla (código 3) si hay columnas personales sin revisar
dbanondump run -c dump/config.yaml --no-log
```

`check --strict` en cada migración es una buena puerta: obliga a decidir (patrón o `{KEEP}`) cada columna
nueva que parezca personal.
