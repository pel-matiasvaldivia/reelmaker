// Servidor de Reel Refurb: entrega la app y maneja las cuentas que pueden usarla.
//
// Sin dependencias externas: compila a un binario estatico que se copia a una
// imagen `scratch`, de modo que la imagen publicada pesa poco mas que el
// binario mas los archivos del sitio.
package main

import (
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func env(clave, porDefecto string) string {
	if v := os.Getenv(clave); v != "" {
		return v
	}
	return porDefecto
}

func main() {
	addr := flag.String("addr", env("REELMAKER_ADDR", ":8080"), "direccion de escucha")
	raiz := flag.String("root", env("REELMAKER_ROOT", "/srv"), "directorio con los archivos del sitio")
	datos := flag.String("data", env("REELMAKER_DATA", "/data"), "directorio donde se guardan las cuentas")
	chequeo := flag.Bool("healthcheck", false, "consulta /healthz en la direccion de escucha y termina")
	flag.Parse()

	if *chequeo {
		if err := healthcheck(*addr); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck:", err)
			os.Exit(1)
		}
		return
	}

	registrarTiposMIME()

	alm, err := AbrirAlmacen(*datos)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			log.Printf("el proceso corre como uid %d y no puede escribir en %s", os.Getuid(), *datos)
			log.Printf("arreglalo con: docker run --rm -v <volumen>:/data busybox chown -R 65532:65532 /data")
		}
		log.Fatalf("no pude abrir el almacen de cuentas: %v", err)
	}
	if err := SembrarAdmin(alm, os.Getenv("REELMAKER_ADMIN_USER"), os.Getenv("REELMAKER_ADMIN_PASSWORD")); err != nil {
		log.Fatalf("no pude crear la cuenta inicial: %v", err)
	}
	sesiones, err := CargarClave(*datos)
	if err != nil {
		log.Fatalf("no pude preparar la clave de sesiones: %v", err)
	}

	app := &App{
		usuarios:  alm,
		sesiones:  sesiones,
		freno:     NuevoFreno(8, 15*time.Minute),
		estaticos: servirEstaticos(*raiz),
		raiz:      *raiz,
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           registrar(seguridad(comprimir(rutas(app)))),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, parar := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer parar()

	go func() {
		log.Printf("reelmaker escuchando en %s (sitio %s, datos %s)", *addr, *raiz, *datos)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("no se pudo escuchar en %s: %v", *addr, err)
		}
	}()

	<-ctx.Done()
	cierre, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelar()
	if err := srv.Shutdown(cierre); err != nil {
		log.Printf("cierre con error: %v", err)
	}
	log.Print("reelmaker detenido")
}

func rutas(app *App) http.Handler {
	mux := http.NewServeMux()

	// Publico.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/login", app.paginaLogin)
	// La hoja de estilos la necesita la propia pagina de ingreso.
	mux.HandleFunc("/ui.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		app.servirArchivo(w, r, "ui.css")
	})
	mux.HandleFunc("/api/login", app.apiLogin)
	mux.HandleFunc("/api/logout", app.apiLogout)
	mux.HandleFunc("/api/me", app.apiYo)

	// Con sesion.
	mux.Handle("/api/password", app.requiereSesion(http.HandlerFunc(app.apiClavePropia)))

	// Solo administradores.
	mux.Handle("/admin", app.requiereAdmin(http.HandlerFunc(app.paginaAdmin)))
	mux.Handle("/api/admin/usuarios", app.requiereAdmin(http.HandlerFunc(app.apiUsuarios)))
	mux.Handle("/api/admin/usuarios/", app.requiereAdmin(http.HandlerFunc(app.apiUsuario)))

	// La app y el resto de los archivos, detras de la sesion.
	mux.Handle("/", app.requiereSesion(app.estaticos))
	return mux
}

func registrarTiposMIME() {
	// Los tipos MIME no vienen con la imagen scratch: se registran a mano.
	for ext, tipo := range map[string]string{
		".html":        "text/html; charset=utf-8",
		".css":         "text/css; charset=utf-8",
		".js":          "text/javascript; charset=utf-8",
		".json":        "application/json; charset=utf-8",
		".svg":         "image/svg+xml",
		".png":         "image/png",
		".jpg":         "image/jpeg",
		".webp":        "image/webp",
		".ico":         "image/x-icon",
		".woff2":       "font/woff2",
		".txt":         "text/plain; charset=utf-8",
		".webmanifest": "application/manifest+json",
	} {
		_ = mime.AddExtensionType(ext, tipo)
	}
}

// servirEstaticos entrega los archivos del sitio y cae en index.html para
// cualquier ruta que no exista, para que la app funcione como una sola pagina.
func servirEstaticos(raiz string) http.Handler {
	archivos := http.Dir(raiz)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "metodo no permitido", http.StatusMethodNotAllowed)
			return
		}
		limpio := path.Clean("/" + r.URL.Path)
		// Las paginas con ruta propia no se sirven tambien por su nombre de archivo.
		switch limpio {
		case "/login.html":
			http.Redirect(w, r, "/login", http.StatusMovedPermanently)
			return
		case "/admin.html":
			http.Redirect(w, r, "/admin", http.StatusMovedPermanently)
			return
		}
		destino := limpio
		if strings.HasSuffix(limpio, "/") {
			destino = path.Join(limpio, "index.html")
		}
		f, err := archivos.Open(destino)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				http.Error(w, "no se pudo leer el archivo", http.StatusInternalServerError)
				return
			}
			destino = "/index.html"
			if f, err = archivos.Open(destino); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			http.Error(w, "no se pudo leer el archivo", http.StatusInternalServerError)
			return
		}
		if info.IsDir() {
			f.Close()
			destino = path.Join(destino, "index.html")
			if f, err = archivos.Open(destino); err != nil {
				http.NotFound(w, r)
				return
			}
			if info, err = f.Stat(); err != nil {
				http.Error(w, "no se pudo leer el archivo", http.StatusInternalServerError)
				return
			}
		}

		if filepath.Ext(destino) == ".html" {
			// El HTML se revalida siempre: asi un deploy nuevo se ve enseguida.
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeContent(w, r, destino, info.ModTime(), f)
	})
}

func seguridad(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// La app arma el video en el navegador: solo necesita sus propios
		// archivos, las tipografias de Google y blobs para el video generado.
		h.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-inline'",
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
			"font-src 'self' https://fonts.gstatic.com",
			"img-src 'self' data: blob:",
			"media-src 'self' blob:",
			"connect-src 'self'",
			"frame-ancestors 'self'",
			"base-uri 'none'",
			"form-action 'none'",
		}, "; "))
		siguiente.ServeHTTP(w, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	gz        *gzip.Writer
	sinCuerpo bool
}

func (g *gzipWriter) WriteHeader(code int) {
	// El largo lo calculo ServeContent sobre el archivo sin comprimir.
	g.ResponseWriter.Header().Del("Content-Length")
	if code == http.StatusNotModified || code == http.StatusNoContent {
		g.sinCuerpo = true
		g.ResponseWriter.Header().Del("Content-Encoding")
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if g.sinCuerpo {
		return len(b), nil
	}
	return g.gz.Write(b)
}

func comprimir(siguiente http.Handler) http.Handler {
	comprimible := func(p string) bool {
		switch filepath.Ext(p) {
		case ".html", ".css", ".js", ".json", ".svg", ".txt", ".webmanifest", "":
			return true
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || !comprimible(r.URL.Path) {
			siguiente.ServeHTTP(w, r)
			return
		}
		// Un Range sobre el archivo sin comprimir no corresponde al cuerpo comprimido.
		r.Header.Del("Range")
		r.Header.Del("If-Range")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		gw := &gzipWriter{ResponseWriter: w, gz: gz}
		defer func() {
			if !gw.sinCuerpo {
				_ = gz.Close()
			}
		}()
		siguiente.ServeHTTP(gw, r)
	})
}

func registrar(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		siguiente.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(inicio).Round(time.Millisecond))
	})
}

func healthcheck(addr string) error {
	host, puerto, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	cliente := &http.Client{Timeout: 3 * time.Second}
	resp, err := cliente.Get("http://" + net.JoinHostPort(host, puerto) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("respuesta %s", resp.Status)
	}
	return nil
}
