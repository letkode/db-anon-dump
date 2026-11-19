# dbanondump

Copia tablas desde una base SQL (o desde un dump) a un **contenedor temporal en Docker**, anonimiza las
columnas que listes en un YAML y escribe un dump listo para desarrollo. El contenedor se destruye siempre
al terminar.

- Un binario estático en Go; lo único que necesitas en el equipo es Docker.
- Tres archivos YAML por proyecto (`config.yaml`, `columns.yaml`, `functions.yaml`); el paquete no sabe nada de tu proyecto.
- Motores: **MySQL 8**, **MariaDB** y **PostgreSQL**. Se declara en el YAML (`engine:`).

> Usa una réplica o un usuario **de solo lectura** en el origen: la herramienta solo lee.
> El dump resultante está anonimizado pero sigue siendo **interno**: no lo publiques.

## Instalar

No necesitas compilar cada vez. Compila **una vez** (o descarga el binario de una release) y úsalo:

```bash
make build                                                   # dist/dbanondump-{linux-amd64,linux-arm64,darwin-arm64}
install -m 0755 dist/dbanondump-linux-amd64 ~/.local/bin/dbanondump   # opcional: dejarlo en el PATH
dbanondump version
```

Solo vuelves a compilar si cambia el código del paquete. Cambiar los YAML nunca requiere compilar.

## Uso

```bash
dbanondump init dump/                             # crea config.yaml, columns.yaml, functions.yaml y .env de ejemplo
$EDITOR dump/.env                                 # la clave va en .env (DBANONDUMP_SOURCE_PASSWORD), NUNCA en config.yaml
dbanondump validate -c config.yaml                # lint offline de los 3 archivos: sin Docker ni base de datos
dbanondump check    -c config.yaml                # valida contra la estructura real y audita
dbanondump run      -c config.yaml                # extrae, anonimiza y escribe el dump
```

```bash
dbanondump run -t user,candidate                  # solo estas tablas (reemplaza tables.include)
dbanondump run -t '*' -x mail,job_offer           # todas menos estas
dbanondump run --from-dump prod.sql.gz            # desde un dump en vez del origen
dbanondump run -o /tmp/dev.sql.gz --parallel 8
dbanondump check --strict                         # exit 3 si hay columnas "personales" sin patrón
```

Cargar el resultado en la base de desarrollo (MySQL / MariaDB / PostgreSQL):

```bash
gunzip -c output/dev-dump.sql.gz | docker exec -i <mysql-container> mysql -uroot -p<password> <database>
gunzip -c output/dev-dump.sql.gz | docker exec -i <pg-container> psql -U postgres -d <database>
```

## La clave (`.env`)

La clave del origen **no** va en `config.yaml`. `dbanondump` la lee de los archivos `.env` que están en la misma
carpeta que `config.yaml` (`init` crea el `.env` a partir de `templates/.env.dist`):

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

- `APP_ENV` se toma del entorno real o, si no está, de `.env` / `.env.local`. Sin `APP_ENV` solo se leen `.env` y `.env.local`.
- Una variable ya exportada en el entorno **gana siempre** sobre todos los archivos (útil en CI: se exporta y no hacen falta archivos).
- Los archivos que no existen se ignoran. Con `source.dump_file` no se necesita clave.
- El nombre de la variable se cambia con `source.password_env` en `config.yaml`.
- Formato `CLAVE=valor`, comentarios `#`, comillas y `export` (lo interpreta [`joho/godotenv`](https://github.com/joho/godotenv)).
  **Usa comillas simples** si la clave tiene `$`: sin comillas o con comillas dobles, `$` inicia una expansión de variable.
- `.env`, `.env.local` y cualquier `.env.<algo>` están en `.gitignore` (excepto `.env.dist`): no los subas al repositorio.

## Logs de ejecución

Cada `run` y `check` escribe un archivo en `logs/<comando>-<fecha-hora>.log` (junto a `config.yaml`), por ejemplo
`logs/run-20261119-153045.log`, con permisos 600:

- las mismas líneas que salen por pantalla, cada una con su hora;
- el error, si falla (con el código de salida);
- un resumen final con el **resultado**, la **duración total** y el **tiempo de cada fase**
  (base temporal, estructura, pre-flight, extracción de datos, anonimización, dump y limpieza), también cuando la ejecución falla.

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

```yaml
logs:            # opcional, en config.yaml
  dir: logs      # relativo a config.yaml; "" = no guardar logs (por defecto: logs)
  keep: 30       # conserva solo los 30 más nuevos (por defecto 0 = conserva todos)
```

Por línea de comandos: `--log-dir DIR` cambia la carpeta y `--no-log` no escribe log. `validate` e `init` no guardan log.

- La clave del origen nunca se escribe en el log (se reemplaza por `***`), pero los errores de SQL pueden citar
  fragmentos de datos reales: por eso el archivo es 600 y `logs/` está en `.gitignore`.
- Los errores anteriores a leer `config.yaml` (archivo inexistente o inválido) solo salen por pantalla.
- Si no se puede escribir el log (permisos, disco lleno) se avisa y la ejecución continúa.

## Los archivos

Un proyecto son tres YAML (más el `.env` con la clave) en una misma carpeta. Para crearlos con plantillas comentadas y con ejemplos, en
la carpeta donde los vayas a usar:

```bash
dbanondump init                 # en la carpeta actual
dbanondump init dump/           # o en otra (se crea si no existe)
dbanondump init --force dump/   # sobrescribe si ya existen
```

`init` también escribe un `.env` (con permisos 600) para la clave del origen; ver [La clave (`.env`)](#la-clave-env).
`init` se niega a tocar nada si alguno de los cuatro archivos ya existe (no deja archivos a medias). Las
plantillas viven en `templates/*.dist` y van embebidas en el binario, así que `init` funciona en cualquier
servidor sin copiar nada más.
 Se le pasa solo `config.yaml` (`-c`), y este apunta a los
otros dos. Cada uno cambia por una razón distinta:

| Archivo | Qué contiene | Cuándo cambia |
|---|---|---|
| `config.yaml` | motor, origen, base temporal, salida, tablas | por ambiente |
| `columns.yaml` | qué columnas se anonimizan y cómo | con el esquema (va en el PR de cada migración) |
| `functions.yaml` | funciones SQL auxiliares que los patrones pueden llamar | casi nunca |

### `config.yaml`

```yaml
version: 1
engine: mysql                 # mysql | mariadb | postgres. Un proyecto = un motor; los patrones van en su SQL

source:                       # la clave va en .env como DBANONDUMP_SOURCE_PASSWORD (cambiable con password_env)
  host: replica.internal
  port: 3306                  # por defecto 3306 (5432 en postgres)
  user: readonly
  database: lablab
  # container_host: host.docker.internal   # host del origen visto desde el contenedor
  # dump_file: ./prod.sql.gz               # alternativa: importar un dump

temp:
  image: mysql:8.0.32         # misma versión mayor que el origen (por defecto, la del motor)
  tmpfs: false                # true = datadir en RAM (más rápido, usa memoria)

output: output/dev-dump-{date}.sql.gz   # {date} = 2026-11-18 · {datetime} = 20261118-153045

tables:
  include: ["*"]              # nombre, glob (user_*) o * para todas
  exclude: [mail, job_offer]  # las exclusiones siempre ganan

columns: columns.yaml         # opcional; por defecto, columns.yaml junto a este archivo (obligatorio que exista)
functions: functions.yaml     # opcional; por defecto, functions.yaml junto a este archivo (si existe)
```

- **`output`** lleva la fecha automáticamente: por defecto `output/dev-dump-{date}.sql.gz`. Funciona igual con
  `-o`. Las rutas relativas se resuelven contra la carpeta de `config.yaml`.
- Una clave desconocida en `config.yaml` es un error (un typo no se ignora).

### `columns.yaml`

Solo lo que se anonimiza; todo lo demás se copia tal cual.

```yaml
"*":                          # comodín: toda tabla extraída que tenga esa columna
  email: "wild-{id}@example.test"
user:
  email: "user-{id}@example.test"
  birthdate: "{MAKEDATE(YEAR(birthdate), 1)}"
  notes: "{NULL}"
  plain_password:
    pattern: "{NULL}"
    optional: true            # no falla si la columna no existe en ese ambiente
```

### `functions.yaml`

Funciones SQL que se crean en la base **temporal** (no van en el dump) para que los patrones las llamen.
Cada `sql` es **una sola sentencia**: no hace falta `DELIMITER`, y el programa hace `DROP FUNCTION IF EXISTS
<name>` antes de crearla.

```yaml
- name: fake_rut
  sql: |
    CREATE FUNCTION fake_rut(p_rut BIGINT) RETURNS VARCHAR(12) DETERMINISTIC NO SQL
    BEGIN
        RETURN CONCAT(p_rut, '-', fake_rut_dv(p_rut));
    END
```

El archivo es opcional: si tus patrones no llaman funciones propias, no lo necesitas. En PostgreSQL el
`DROP FUNCTION IF EXISTS <name>` falla si el nombre tiene varias sobrecargas; usa nombres únicos.

Plantillas con ejemplos: `dbanondump init` (ver arriba). Proyectos de ejemplo mínimos, uno por motor:
`testdata/generic/<motor>/`.

### Patrones

Un patrón es texto con **expresiones SQL entre llaves**. Lo de afuera es literal; lo de adentro se copia tal
cual y debe ser SQL válido **del motor declarado**.

| Patrón | Resultado |
|---|---|
| `user-{id}@example.test` | `user-15@example.test` |
| `+56900{id}` | `+5690015` |
| `{MAKEDATE(YEAR(birthdate), 1)}` | 1 de enero del mismo año (MySQL / MariaDB) |
| `{make_date(EXTRACT(YEAR FROM birthdate)::int, 1, 1)}` | lo mismo en PostgreSQL |
| `{NULL}` | `NULL`; si la columna es `NOT NULL`, usa `''`, `0`, `'{}'`, `false`, `1970-01-01`… según el tipo |
| `{KEEP}` | revisada: se deja tal cual (silencia la auditoría) |
| `{REGEXP_REPLACE(...)}` | enmascara correos y teléfonos **dentro** de un texto libre y conserva el resto; ver el ejemplo en `columns.yaml.dist` |

Reglas que se aplican solas:

- **Conserva los `NULL`**: si el valor original es `NULL`, sigue `NULL`.
- **Determinista**: usa `{id}` (u otra columna como `{user_id}`) para que el mismo dato en dos tablas
  coincida y los JOIN sigan funcionando. Refiérete solo a columnas **no anonimizadas** o a la propia columna.
- **Columnas `UNIQUE` de texto**: se "aparcan" primero (`t-<id>`) para que el valor final no choque con uno
  real. Requiere una columna `id`; si falta, el pre-vuelo lo informa.
- **Verificación**: después de cada tabla se cuentan las filas que no coinciden con su patrón; debe dar `0`.
- Una entrada exacta (`user.email`) gana sobre el comodín (`"*".email`).
- Una entrada para una tabla que **no** extraes se ignora; para una tabla que sí extraes con una columna que
  no existe, falla (un typo dejaría el dato sin tratar).

## Qué hace, en orden

1. Levanta el contenedor temporal con clave aleatoria, publicado solo en `127.0.0.1`.
2. Resuelve `tables` y baja **solo la estructura** de esas tablas.
3. **Pre-vuelo** (en segundos, antes de mover datos): comprueba cada patrón contra la estructura real
   (funciones inexistentes, columnas que no existen, errores de tipo, columnas `UNIQUE` sin `id`).
   **Reporta todos los errores juntos** (exit 3).
4. Audita: lista columnas que parecen personales (JSON, texto, nombres como email/phone/rut/name…) y no
   tienen patrón. Con `--strict` es un error.
5. Baja los datos y verifica que ningún patrón produzca más caracteres de los que admite la columna.
6. Anonimiza las tablas **en paralelo**, cada una con su verificación.
7. Escribe el dump (sin funciones ni triggers; sin archivo parcial si algo falla) y destruye el contenedor.

Códigos de salida: `0` ok · `2` uso o archivo inválido · `3` problema de config/pre-vuelo o `--strict` ·
`5` falló una sentencia de anonimización · `6` falló una verificación · `7` falló la extracción o importación.

## Notas por motor

| Motor | Imagen por defecto | Herramientas | Notas |
|---|---|---|---|
| `mysql` | `mysql:8.0.32` | `mysqldump`, `mysql` | |
| `mariadb` | `mariadb:10.11` | `mariadb-dump`, `mariadb` | Las columnas JSON son `LONGTEXT` con un `CHECK`: si una es `NOT NULL`, escribe un patrón válido explícito, por ejemplo `"{'{}'}"`. |
| `postgres` | `postgres:16` | `pg_dump`, `psql` | Esquema `public`. `pg_dump -t` no sigue dependencias: si una tabla extraída tiene una FK a una tabla **no** extraída, no se puede restaurar. Extrae también la referenciada. Los patrones usan SQL de PostgreSQL (`MOD(id, 25)`, `make_date`…). |

## Un límite que debes conocer

La herramienta **solo anonimiza lo que está en el YAML**; una columna que no aparece se copia tal cual. Por eso
existen `check` y `--strict`: córrelos cuando cambie el esquema y revisa la lista antes de compartir un dump.
Para cada columna nueva con texto libre o JSON, o con un nombre como email/phone/rut/name, agrega un patrón o
márcala `{KEEP}`.

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
