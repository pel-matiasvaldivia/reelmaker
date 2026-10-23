# syntax=docker/dockerfile:1

# Etapa de compilacion: corre siempre en la arquitectura del runner y cruza
# compila para la arquitectura de destino, asi el build multi-arch no necesita QEMU.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY server/go.mod ./
COPY server/*.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/reelmaker .
# Directorio de datos vacio con el dueno correcto: cuando Docker crea el volumen
# por primera vez, copia estos permisos, y el contenedor puede escribir ahi
# aunque el resto del filesystem este en modo lectura.
RUN mkdir -p /vacio/data && chown -R 65532:65532 /vacio/data

# Imagen final: solo el binario estatico, los archivos del sitio y el directorio de datos.
FROM scratch
LABEL org.opencontainers.image.title="Reel Refurb" \
      org.opencontainers.image.description="Armador de reels verticales para equipos refurbished, con cuentas de usuario" \
      org.opencontainers.image.source="https://github.com/pel-matiasvaldivia/reelmaker" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/reelmaker /reelmaker
COPY --from=build /vacio/data /data
COPY app/ /srv/
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
ENV REELMAKER_ADDR=":8080" \
    REELMAKER_ROOT="/srv" \
    REELMAKER_DATA="/data"
HEALTHCHECK --interval=30s --timeout=3s --start-period=2s --retries=3 \
    CMD ["/reelmaker", "-healthcheck"]
ENTRYPOINT ["/reelmaker"]
