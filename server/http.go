package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// App reune las dependencias de los handlers.
type App struct {
	usuarios  *Almacen
	sesiones  *Sesiones
	freno     *Freno
	estaticos http.Handler
	raiz      string
}

type claveCtx struct{}

// conUsuario y usuarioCtx pasan la cuenta autenticada al resto de la cadena.
func conUsuario(r *http.Request, u *Usuario) context.Context {
	return context.WithValue(r.Context(), claveCtx{}, u)
}

func usuarioCtx(r *http.Request) *Usuario {
	u, _ := r.Context().Value(claveCtx{}).(*Usuario)
	return u
}

// servirArchivo entrega una pagina concreta del directorio del sitio.
func (a *App) servirArchivo(w http.ResponseWriter, r *http.Request, nombre string) {
	http.ServeFile(w, r, filepath.Join(a.raiz, nombre))
}

func responder(w http.ResponseWriter, codigo int, cuerpo any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(codigo)
	if cuerpo != nil {
		_ = json.NewEncoder(w).Encode(cuerpo)
	}
}

func error1(w http.ResponseWriter, codigo int, mensaje string) {
	responder(w, codigo, map[string]string{"error": mensaje})
}

// pedidoPropio corta los pedidos que cambian datos y no vienen de la propia pagina.
// La cookie es SameSite=Lax, asi que un sitio ajeno no puede mandarla en un POST;
// la cabecera y el Origin cierran el resto de los casos.
func (a *App) pedidoPropio(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Reelmaker") != "1" {
		error1(w, http.StatusForbidden, "pedido sin la cabecera X-Reelmaker")
		return false
	}
	if origen := r.Header.Get("Origin"); origen != "" {
		u, err := url.Parse(origen)
		if err != nil || u.Host != r.Host {
			error1(w, http.StatusForbidden, "origen no permitido")
			return false
		}
	}
	return true
}

// usuarioDe devuelve la cuenta detras de la cookie, si sigue activa y la version coincide.
func (a *App) usuarioDe(r *http.Request) *Usuario {
	ck, err := r.Cookie(nombreCookie)
	if err != nil {
		return nil
	}
	id, version, ok := a.sesiones.Leer(ck.Value)
	if !ok {
		return nil
	}
	u, existe := a.usuarios.PorID(id)
	if !existe || !u.Activo || u.Version != version {
		return nil
	}
	return u
}

func (a *App) requiereSesion(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := a.usuarioDe(r)
		if u == nil {
			borrarCookie(w, r)
			if esNavegacion(r) {
				destino := "/login"
				if r.URL.Path != "/" {
					destino += "?next=" + url.QueryEscape(r.URL.RequestURI())
				}
				http.Redirect(w, r, destino, http.StatusSeeOther)
				return
			}
			error1(w, http.StatusUnauthorized, "hay que iniciar sesion")
			return
		}
		siguiente.ServeHTTP(w, r.WithContext(conUsuario(r, u)))
	})
}

func (a *App) requiereAdmin(siguiente http.Handler) http.Handler {
	return a.requiereSesion(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := usuarioCtx(r); u == nil || u.Rol != RolAdmin {
			if esNavegacion(r) {
				http.Error(w, "esta seccion es solo para administradores", http.StatusForbidden)
				return
			}
			error1(w, http.StatusForbidden, "esta seccion es solo para administradores")
			return
		}
		siguiente.ServeHTTP(w, r)
	}))
}

func esNavegacion(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html") || r.Header.Get("Accept") == ""
}

/* ---------- ingreso y salida ---------- */

func (a *App) paginaLogin(w http.ResponseWriter, r *http.Request) {
	if a.usuarioDe(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.servirArchivo(w, r, "login.html")
}

func (a *App) apiLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		error1(w, http.StatusMethodNotAllowed, "usa POST")
		return
	}
	if !a.pedidoPropio(w, r) {
		return
	}
	var cuerpo struct {
		Usuario string `json:"usuario"`
		Clave   string `json:"clave"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&cuerpo); err != nil {
		error1(w, http.StatusBadRequest, "cuerpo invalido")
		return
	}
	if !a.freno.Permitido(cuerpo.Usuario, r) {
		error1(w, http.StatusTooManyRequests, "demasiados intentos; espera unos minutos")
		return
	}
	u, existe := a.usuarios.PorUsuario(cuerpo.Usuario)
	// Siempre se verifica un hash, exista o no la cuenta, para no delatar cuales existen.
	hash := "pbkdf2$sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if existe {
		hash = u.Hash
	}
	if !Verificar(hash, cuerpo.Clave) || !existe {
		a.freno.Falla(cuerpo.Usuario, r)
		error1(w, http.StatusUnauthorized, "usuario o contrasena incorrectos")
		return
	}
	if !u.Activo {
		a.freno.Falla(cuerpo.Usuario, r)
		error1(w, http.StatusForbidden, "esta cuenta esta deshabilitada")
		return
	}
	a.freno.Exito(cuerpo.Usuario, r)
	a.usuarios.MarcarIngreso(u.ID)
	valor, vence := a.sesiones.Emitir(u)
	ponerCookie(w, r, valor, vence)
	log.Printf("ingreso de %s (%s)", u.Usuario, u.Rol)
	responder(w, http.StatusOK, map[string]any{"usuario": u.Publico()})
}

func (a *App) apiLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		error1(w, http.StatusMethodNotAllowed, "usa POST")
		return
	}
	if !a.pedidoPropio(w, r) {
		return
	}
	borrarCookie(w, r)
	responder(w, http.StatusOK, map[string]string{"estado": "sesion cerrada"})
}

func (a *App) apiYo(w http.ResponseWriter, r *http.Request) {
	u := a.usuarioDe(r)
	if u == nil {
		error1(w, http.StatusUnauthorized, "hay que iniciar sesion")
		return
	}
	responder(w, http.StatusOK, map[string]any{"usuario": u.Publico()})
}

func (a *App) apiClavePropia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		error1(w, http.StatusMethodNotAllowed, "usa POST")
		return
	}
	if !a.pedidoPropio(w, r) {
		return
	}
	u := usuarioCtx(r)
	var cuerpo struct {
		Actual string `json:"actual"`
		Nueva  string `json:"nueva"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&cuerpo); err != nil {
		error1(w, http.StatusBadRequest, "cuerpo invalido")
		return
	}
	completo, ok := a.usuarios.PorID(u.ID)
	if !ok || !Verificar(completo.Hash, cuerpo.Actual) {
		error1(w, http.StatusUnauthorized, "la contrasena actual no coincide")
		return
	}
	if _, err := a.usuarios.Actualizar(u.ID, Cambios{Clave: &cuerpo.Nueva}); err != nil {
		a.errorDeAlmacen(w, err)
		return
	}
	// El cambio de clave sube la version: hay que volver a emitir la cookie.
	nuevo, _ := a.usuarios.PorID(u.ID)
	valor, vence := a.sesiones.Emitir(nuevo)
	ponerCookie(w, r, valor, vence)
	responder(w, http.StatusOK, map[string]string{"estado": "contrasena actualizada"})
}

/* ---------- panel de administracion ---------- */

func (a *App) paginaAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.servirArchivo(w, r, "admin.html")
}

func (a *App) apiUsuarios(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		lista := a.usuarios.Lista()
		publicos := make([]map[string]any, len(lista))
		for i, u := range lista {
			publicos[i] = u.Publico()
		}
		responder(w, http.StatusOK, map[string]any{"usuarios": publicos})
	case http.MethodPost:
		if !a.pedidoPropio(w, r) {
			return
		}
		var cuerpo struct {
			Usuario string `json:"usuario"`
			Nombre  string `json:"nombre"`
			Rol     string `json:"rol"`
			Clave   string `json:"clave"`
			Activo  *bool  `json:"activo"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&cuerpo); err != nil {
			error1(w, http.StatusBadRequest, "cuerpo invalido")
			return
		}
		activo := true
		if cuerpo.Activo != nil {
			activo = *cuerpo.Activo
		}
		if cuerpo.Rol == "" {
			cuerpo.Rol = RolEditor
		}
		u, err := a.usuarios.Crear(cuerpo.Usuario, cuerpo.Nombre, cuerpo.Rol, cuerpo.Clave, activo)
		if err != nil {
			a.errorDeAlmacen(w, err)
			return
		}
		log.Printf("%s creo la cuenta %s (%s)", usuarioCtx(r).Usuario, u.Usuario, u.Rol)
		responder(w, http.StatusCreated, map[string]any{"usuario": u.Publico()})
	default:
		error1(w, http.StatusMethodNotAllowed, "usa GET o POST")
	}
}

func (a *App) apiUsuario(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/usuarios/"), "/")
	if id == "" {
		error1(w, http.StatusNotFound, "falta el id del usuario")
		return
	}
	yo := usuarioCtx(r)

	switch r.Method {
	case http.MethodPatch:
		if !a.pedidoPropio(w, r) {
			return
		}
		var cuerpo struct {
			Nombre *string `json:"nombre"`
			Rol    *string `json:"rol"`
			Activo *bool   `json:"activo"`
			Clave  *string `json:"clave"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&cuerpo); err != nil {
			error1(w, http.StatusBadRequest, "cuerpo invalido")
			return
		}
		if id == yo.ID {
			if cuerpo.Activo != nil && !*cuerpo.Activo {
				error1(w, http.StatusConflict, "no podes deshabilitar tu propia cuenta")
				return
			}
			if cuerpo.Rol != nil && *cuerpo.Rol != RolAdmin {
				error1(w, http.StatusConflict, "no podes quitarte el rol de administrador")
				return
			}
		}
		u, err := a.usuarios.Actualizar(id, Cambios{Nombre: cuerpo.Nombre, Rol: cuerpo.Rol, Activo: cuerpo.Activo, Clave: cuerpo.Clave})
		if err != nil {
			a.errorDeAlmacen(w, err)
			return
		}
		log.Printf("%s edito la cuenta %s", yo.Usuario, u.Usuario)
		responder(w, http.StatusOK, map[string]any{"usuario": u.Publico()})
	case http.MethodDelete:
		if !a.pedidoPropio(w, r) {
			return
		}
		if id == yo.ID {
			error1(w, http.StatusConflict, "no podes borrar tu propia cuenta")
			return
		}
		if err := a.usuarios.Borrar(id); err != nil {
			a.errorDeAlmacen(w, err)
			return
		}
		log.Printf("%s borro la cuenta %s", yo.Usuario, id)
		responder(w, http.StatusOK, map[string]string{"estado": "cuenta borrada"})
	default:
		error1(w, http.StatusMethodNotAllowed, "usa PATCH o DELETE")
	}
}

func (a *App) errorDeAlmacen(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoExiste):
		error1(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrUsuarioRepetido), errors.Is(err, ErrUltimoAdmin):
		error1(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrUsuarioInvalido), errors.Is(err, ErrClaveCorta), errors.Is(err, ErrRolInvalido):
		error1(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("error al guardar usuarios: %v", err)
		error1(w, http.StatusInternalServerError, "no se pudo guardar el cambio")
	}
}

/* ---------- primer administrador ---------- */

// SembrarAdmin crea la primera cuenta si el almacen esta vacio.
func SembrarAdmin(alm *Almacen, usuario, clave string) error {
	if !alm.Vacio() {
		return nil
	}
	if usuario == "" {
		usuario = "admin"
	}
	generada := false
	if clave == "" {
		clave = ClaveAlAzar()
		generada = true
	}
	u, err := alm.Crear(usuario, "Administrador", RolAdmin, clave, true)
	if err != nil {
		return err
	}
	if generada {
		log.Printf("--------------------------------------------------------------")
		log.Printf("primer ingreso: usuario %q, contrasena %q", u.Usuario, clave)
		log.Printf("cambiala desde el panel despues de entrar; no vuelve a mostrarse")
		log.Printf("--------------------------------------------------------------")
	} else {
		log.Printf("cuenta inicial %q creada desde las variables de entorno", u.Usuario)
	}
	return nil
}
