package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	nombreCookie   = "reelmaker_sesion"
	duracionSesion = 30 * 24 * time.Hour
)

// Sesiones firma y valida cookies. No guarda estado: la cookie lleva el id del
// usuario, la version de sus credenciales y el vencimiento, y cada pedido
// vuelve a leer la cuenta. Asi, desactivar a alguien corta el acceso enseguida.
type Sesiones struct {
	clave []byte
}

// CargarClave usa REELMAKER_SECRET si esta definida; si no, genera una clave y
// la guarda en el directorio de datos para que las sesiones sobrevivan a un reinicio.
func CargarClave(dir string) (*Sesiones, error) {
	if v := strings.TrimSpace(os.Getenv("REELMAKER_SECRET")); v != "" {
		return &Sesiones{clave: []byte(v)}, nil
	}
	ruta := filepath.Join(dir, "secret")
	crudo, err := os.ReadFile(ruta)
	if err == nil && len(strings.TrimSpace(string(crudo))) >= 32 {
		return &Sesiones{clave: []byte(strings.TrimSpace(string(crudo)))}, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	clave := base64.RawStdEncoding.EncodeToString(b)
	if err := os.WriteFile(ruta, []byte(clave+"\n"), 0o600); err != nil {
		return nil, err
	}
	return &Sesiones{clave: []byte(clave)}, nil
}

func (s *Sesiones) firmar(cuerpo string) string {
	m := hmac.New(sha256.New, s.clave)
	m.Write([]byte(cuerpo))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Emitir devuelve el valor de la cookie para un usuario.
func (s *Sesiones) Emitir(u *Usuario) (string, time.Time) {
	vence := time.Now().Add(duracionSesion)
	cuerpo := u.ID + "|" + strconv.Itoa(u.Version) + "|" + strconv.FormatInt(vence.Unix(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(cuerpo)) + "." + s.firmar(cuerpo), vence
}

// Leer valida la firma y el vencimiento y devuelve el id y la version de la cookie.
func (s *Sesiones) Leer(valor string) (id string, version int, ok bool) {
	punto := strings.LastIndex(valor, ".")
	if punto <= 0 {
		return "", 0, false
	}
	crudo, err := base64.RawURLEncoding.DecodeString(valor[:punto])
	if err != nil {
		return "", 0, false
	}
	cuerpo := string(crudo)
	if !hmac.Equal([]byte(s.firmar(cuerpo)), []byte(valor[punto+1:])) {
		return "", 0, false
	}
	partes := strings.Split(cuerpo, "|")
	if len(partes) != 3 {
		return "", 0, false
	}
	version, err = strconv.Atoi(partes[1])
	if err != nil {
		return "", 0, false
	}
	vence, err := strconv.ParseInt(partes[2], 10, 64)
	if err != nil || time.Now().Unix() > vence {
		return "", 0, false
	}
	return partes[0], version, true
}

func seguro(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func ponerCookie(w http.ResponseWriter, r *http.Request, valor string, vence time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     nombreCookie,
		Value:    valor,
		Path:     "/",
		Expires:  vence,
		MaxAge:   int(time.Until(vence).Seconds()),
		HttpOnly: true,
		Secure:   seguro(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func borrarCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     nombreCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   seguro(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// Freno limita los intentos de ingreso fallidos por usuario y direccion.
type Freno struct {
	mu       sync.Mutex
	intentos map[string]*registro
	maximo   int
	ventana  time.Duration
}

type registro struct {
	fallas int
	desde  time.Time
}

func NuevoFreno(maximo int, ventana time.Duration) *Freno {
	return &Freno{intentos: map[string]*registro{}, maximo: maximo, ventana: ventana}
}

func (f *Freno) clave(usuario string, r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return strings.ToLower(usuario) + "|" + ip
}

// Permitido indica si todavia quedan intentos para esa combinacion.
func (f *Freno) Permitido(usuario string, r *http.Request) bool {
	k := f.clave(usuario, r)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limpiarLocked()
	reg := f.intentos[k]
	return reg == nil || reg.fallas < f.maximo
}

func (f *Freno) Falla(usuario string, r *http.Request) {
	k := f.clave(usuario, r)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limpiarLocked()
	if reg := f.intentos[k]; reg != nil {
		reg.fallas++
		return
	}
	f.intentos[k] = &registro{fallas: 1, desde: time.Now()}
}

func (f *Freno) Exito(usuario string, r *http.Request) {
	k := f.clave(usuario, r)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.intentos, k)
}

func (f *Freno) limpiarLocked() {
	corte := time.Now().Add(-f.ventana)
	for k, reg := range f.intentos {
		if reg.desde.Before(corte) {
			delete(f.intentos, k)
		}
	}
}
