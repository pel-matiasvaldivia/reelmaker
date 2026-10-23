# Reel Refurb

Armador de reels verticales para equipos refurbished. Cargás las fotos, completás la ficha y la app genera un video 9:16 listo para TikTok, reels de Instagram y estado de WhatsApp, más el texto de la publicación.

Todo el trabajo pasa en el navegador: las fotos nunca se suben a ningún servidor. El contenedor sólo entrega los archivos estáticos.

## Qué hace

- **Dos modos de reel**
  - *Un equipo*: apertura, una toma por foto con una especificación destacada en cada una, placa de precio con el "antes" tachado y cierre con tus datos.
  - *Varios equipos*: portada con el total de equipos y el "desde $", una placa por equipo con su precio, ficha corta y grado, y un cierre único. Hasta tres fotos por equipo.
- Lista de equipos reordenable, con agregar, duplicar y quitar. Los datos de texto quedan guardados en el navegador; las fotos duran lo que dure la sesión.
- Tres paletas (Cobalto, Taller, Papel) y tres formatos (9:16 para reel/TikTok/estado, 4:5 y 1:1 para feed).
- Música opcional desde un archivo propio, con fundido de entrada y salida.
- Exporta el video (MP4 donde el navegador lo soporta, si no WebM) y la portada en PNG.
- Texto de publicación ya redactado para TikTok, Instagram y WhatsApp, con hashtags y botón de copiar.

## Correrlo en tu VPS

Requiere Docker con el plugin Compose.

```sh
git clone https://github.com/pel-matiasvaldivia/reelmaker.git
cd reelmaker
cp .env.example .env          # ajustá puerto, imagen y dominio
docker compose pull
docker compose up -d
```

Queda en `http://IP-DEL-VPS:8080`. Para ver los logs: `docker compose logs -f reelmaker`.

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

El servidor agrega `Cache-Control: no-cache` al HTML y un año inmutable al resto, comprime con gzip el texto y manda cabeceras de seguridad, entre ellas una CSP que sólo habilita los archivos propios, las tipografías de Google y los blobs del video generado.

GitHub Actions (`.github/workflows/docker.yml`) construye `linux/amd64` y `linux/arm64` en cada push y publica en `ghcr.io/pel-matiasvaldivia/reelmaker` con las etiquetas `latest` (rama por defecto), el nombre de la rama, `sha-<commit>` y, si hay tag `vX.Y.Z`, la versión. El resumen de cada corrida informa el peso de las capas por arquitectura.

## Desarrollo

Sin Docker, con Go instalado:

```sh
cd server && go run . -root ../app -addr :8080
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

## Licencia

MIT. Ver [LICENSE](LICENSE).
