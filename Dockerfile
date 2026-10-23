# syntax=docker/dockerfile:1

# Etapa de compilacion: corre siempre en la arquitectura del runner y cruza
# compila para la arquitectura de destino, asi el build multi-arch no necesita QEMU.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY server/go.mod ./
COPY server/*.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/reelmaker .

# Imagen final: solo el binario estatico y los archivos del sitio.
FROM scratch
LABEL org.opencontainers.image.title="Reel Refurb" \
      org.opencontainers.image.description="Armador de reels verticales para equipos refurbished" \
      org.opencontainers.image.source="https://github.com/pel-matiasvaldivia/reelmaker" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/reelmaker /reelmaker
COPY app/ /srv/
USER 65532:65532
EXPOSE 8080
ENV REELMAKER_ADDR=":8080" \
    REELMAKER_ROOT="/srv"
HEALTHCHECK --interval=30s --timeout=3s --start-period=2s --retries=3 \
    CMD ["/reelmaker", "-healthcheck"]
ENTRYPOINT ["/reelmaker"]
