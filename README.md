# Reel Refurb

Armador de reels verticales para equipos refurbished. Cargás las fotos, completás la ficha y la app genera un video 9:16 listo para TikTok, reels de Instagram y estado de WhatsApp, más el texto de la publicación.

Todo el trabajo de video pasa en el navegador: las fotos nunca se suben a ningún servidor. El contenedor entrega la app y maneja las cuentas que pueden usarla.

## Qué hace

- **Dos modos de reel**
  - *Un equipo*: apertura, una toma por foto con una especificación destacada en cada una, placa de precio con el "antes" tachado y cierre con tus datos.
  - *Varios equipos*: portada con el total de equipos y el "desde $", una placa por equipo con su precio, ficha corta y grado, y un cierre único. Hasta tres fotos por equipo.
- Lista de equipos reordenable, con agregar, duplicar y quitar. Los datos de texto quedan guardados en el navegador; las fotos duran lo que dure la sesión.
- Tres paletas (Cobalto, Taller, Papel) y tres formatos (9:16 para reel/TikTok/estado, 4:5 y 1:1 para feed).
- Música opcional desde un archivo propio, con fundido de entrada y salida.
- Exporta el video (MP4 donde el navegador lo soporta, si no WebM) y la portada en PNG.
- Texto de publicación ya redactado para TikTok, Instagram y WhatsApp, con hashtags y botón de copiar.

## Cuentas y panel de administración

La versión autoalojada pide usuario y contraseña. Hay dos roles:

- **editor**: entra a la herramienta y nada más.
- **admin**: además abre `/admin`, donde ve las cuentas y puede crear, habilitar, deshabilitar, cambiar el rol, resetear la contraseña y borrar.

Deshabilitar una cuenta corta el acceso en el momento: la cookie no guarda permisos, cada pedido vuelve a leer la cuenta, así que la sesión abierta de esa persona deja de valer en el pedido siguiente. Cambiar el rol o la contraseña de alguien también cierra sus sesiones.

En el primer arranque se crea la cuenta inicial. Si definís `REELMAKER_ADMIN_USER` y `REELMAKER_ADMIN_PASSWORD` en `.env`, usa esos datos; si los dejás vacíos, genera una contraseña al azar y la escribe una sola vez en los logs:

```sh
docker compose logs reelmaker | grep -A2 "primer ingreso"
```

Entrá con esa cuenta, cambiá la contraseña en *Mi contraseña* dentro de `/admin` y creá desde ahí la cuenta de la otra persona (rol editor). Pasale el usuario y la contraseña por un canal privado; la app no manda mails.

Detalles de implementación, por si los necesitás auditar:

- Contraseñas con PBKDF2-HMAC-SHA256, 600.000 iteraciones y salt de 16 bytes por cuenta (`crypto/pbkdf2` de la biblioteca estándar). Nunca se guarda ni se devuelve la contraseña.
- Sesión en una cookie `HttpOnly`, `SameSite=Lax` y `Secure` cuando la conexión es HTTPS (detecta `X-Forwarded-Proto`, por eso funciona detrás de Caddy), firmada con HMAC-SHA256. Dura 30 días.
- Los pedidos que cambian datos exigen la cabecera `X-Reelmaker: 1` y, si viene `Origin`, que coincida con el host.
- Ocho intentos fallidos de ingreso por usuario y dirección IP en 15 minutos devuelven 429.
- Las cuentas viven en `/data/usuarios.json` (permisos 0600) dentro del volumen `reelmaker-datos`, junto con la clave de firma. Es el único lugar donde el contenedor escribe: el resto del filesystem va en modo lectura.
- Siempre queda al menos un administrador activo, y nadie puede deshabilitarse, degradarse ni borrarse a sí mismo.

El archivo `app/index.html` sigue funcionando solo: si lo abrís directamente, la barra de sesión no aparece y la herramienta trabaja igual. Las cuentas son de la versión servida.

## Correrlo en tu VPS

Requiere Docker con el plugin Compose.

```sh
git clone https://github.com/pel-matiasvaldivia/reelmaker.git
cd reelmaker
cp .env.example .env          # ajustá puerto, imagen y dominio
docker compose pull
docker compose up -d
```

Queda en `http://IP-DEL-VPS:8080`, que redirige a `/login`. Las credenciales del primer administrador salen de `.env` o, si lo dejaste vacío, de los logs (ver más abajo). Para seguir los logs: `docker compose logs -f reelmaker`.

Las cuentas quedan en el volumen `reelmaker-datos`. Respaldalo si no querés volver a crearlas:

```sh
docker run --rm -v reelmaker_reelmaker-datos:/d -v "$PWD":/out alpine tar czf /out/cuentas.tgz -C /d .
```

### Con dominio y HTTPS

Apuntá el dominio por DNS a la IP del VPS, poné ese dominio en `REELMAKER_DOMAIN` dentro de `.env` y levantá el perfil `tls`:

```sh
docker compose --profile tls up -d
```

Caddy pide y renueva el certificado solo. Con este perfil la app no necesita estar publicada en el puerto 8080: podés borrar el bloque `ports` del servicio `reelmaker` para que sólo quede accesible a través de Caddy.

### Si la imagen es privada

Los paquetes de GHCR nacen privados. Dos caminos:

1. **Hacerla pública** (recomendado para esto): en GitHub, pestaña *Packages* → `reelmaker` → *Package settings* → *Change visibility* → Public. Después `docker compose pull` funciona sin credenciales.
2. **Entrar al registro desde el VPS**, con un token clásico que tenga permiso `read:packages`:

```sh
echo TU_TOKEN | docker login ghcr.io -u TU_USUARIO --password-stdin
```

### Actualizar

```sh
docker compose pull && docker compose up -d
```

Si preferís no moverte de una versión concreta, fijá la etiqueta en `.env`, por ejemplo `REELMAKER_IMAGE=ghcr.io/pel-matiasvaldivia/reelmaker:sha-1a2b3c4`.

## La imagen

`Dockerfile` usa dos etapas: compila el servidor estático con `golang:1.23-alpine` (cruzando a la arquitectura de destino, sin QEMU) y copia el binario a una imagen `scratch` junto con `app/`. No hay shell, ni gestor de paquetes, ni libc dentro del contenedor: la imagen final son dos capas, el binario de unos pocos MB y el HTML.

El contenedor corre como UID 65532, con el filesystem en modo lectura, sin capabilities y con `no-new-privileges`. El healthcheck es el propio binario (`/reelmaker -healthcheck`), porque en `scratch` no hay `curl` ni `wget`.

Las cuentas se guardan en el volumen montado en `/data`; el directorio viene creado en la imagen con dueño 65532, así Docker le pone ese dueño al volumen la primera vez y el contenedor puede escribir ahí con el resto del filesystem en modo lectura.

El servidor agrega `Cache-Control: no-cache` al HTML y un año inmutable al resto, comprime con gzip el texto y manda cabeceras de seguridad, entre ellas una CSP que sólo habilita los archivos propios, las tipografías de Google y los blobs del video generado.

GitHub Actions (`.github/workflows/docker.yml`) construye `linux/amd64` y `linux/arm64` en cada push y publica en `ghcr.io/pel-matiasvaldivia/reelmaker` con las etiquetas `latest` (rama por defecto), el nombre de la rama, `sha-<commit>` y, si hay tag `vX.Y.Z`, la versión. El resumen de cada corrida informa el peso de las capas por arquitectura.

## Desarrollo

Sin Docker, con Go instalado:

```sh
cd server
REELMAKER_ADMIN_USER=jefa REELMAKER_ADMIN_PASSWORD=una-clave-larga \
  go run . -root ../app -data ./datos-dev -addr :8080
go test ./...
```

O directamente abriendo `app/index.html` en el navegador: la app no necesita servidor, el contenedor sólo sirve para publicarla.

Compilar la imagen a mano:

```sh
docker build -t reelmaker:local .
docker run --rm -p 8080:8080 reelmaker:local
```

`app/index.html` es un archivo único sin dependencias de build. El tramo entre los marcadores `artifact:start` y `artifact:end` es el que se publica como artifact en claude.ai; `tools/make-artifact.sh` lo extrae.

## Límites conocidos

- La grabación es en tiempo real: un reel de 20 segundos tarda 20 segundos y hay que dejar la pestaña visible.
- El formato de salida depende del navegador. Chrome y Edge dan MP4 (H.264); Firefox entrega WebM, que TikTok e Instagram aceptan pero WhatsApp no siempre.
- Para que el reel sirva como estado de WhatsApp conviene no pasar de 60 segundos: la app avisa cuando el guión los supera.
- Las tipografías se cargan desde Google Fonts. Sin internet la página funciona igual, con la familia de respaldo del sistema.
- No hay recuperación de contraseña por mail: si alguien la pierde, un administrador se la resetea desde `/admin`.
- Sin HTTPS la contraseña viaja en claro por la red. Para uso real conviene el perfil `tls` o un proxy con certificado adelante.

## Licencia

MIT. Ver [LICENSE](LICENSE).
