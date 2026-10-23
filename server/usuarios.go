package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Iteraciones de PBKDF2-HMAC-SHA256 recomendadas por OWASP para 2024 en adelante.
const iteracionesPBKDF2 = 600000

const (
	RolAdmin  = "admin"
	RolEditor = "editor"
)

var (
	ErrNoExiste        = errors.New("el usuario no existe")
	ErrUsuarioRepetido = errors.New("ya hay un usuario con ese nombre")
	ErrUsuarioInvalido = errors.New("el nombre de usuario admite entre 3 y 32 caracteres: letras, numeros, punto, guion y guion bajo")
	ErrClaveCorta      = errors.New("la contrasena necesita al menos 10 caracteres")
	ErrRolInvalido     = errors.New("el rol tiene que ser admin o editor")
	ErrUltimoAdmin     = errors.New("tiene que quedar al menos un administrador activo")
)

// Usuario es una cuenta con acceso a la herramienta.
type Usuario struct {
	ID            string     `json:"id"`
	Usuario       string     `json:"usuario"`
	Nombre        string     `json:"nombre"`
	Rol           string     `json:"rol"`
	Activo        bool       `json:"activo"`
	Hash          string     `json:"hash"`
	Version       int        `json:"version"` // sube para invalidar las sesiones abiertas
	Creado        time.Time  `json:"creado"`
	UltimoIngreso *time.Time `json:"ultimoIngreso,omitempty"`
}

// Publico devuelve la vista del usuario que se puede mandar al navegador: sin hash.
func (u *Usuario) Publico() map[string]any {
	m := map[string]any{
		"id":      u.ID,
		"usuario": u.Usuario,
		"nombre":  u.Nombre,
		"rol":     u.Rol,
		"activo":  u.Activo,
		"creado":  u.Creado.UTC().Format(time.RFC3339),
	}
	if u.UltimoIngreso != nil {
		m["ultimoIngreso"] = u.UltimoIngreso.UTC().Format(time.RFC3339)
	}
	return m
}

type archivo struct {
	Version  int        `json:"version"`
	Usuarios []*Usuario `json:"usuarios"`
}

// Almacen guarda las cuentas en un unico archivo JSON. Todo el acceso pasa por
// el mutex: un solo proceso escribe, y cada guardado es atomico (temporal mas rename).
type Almacen struct {
	mu    sync.RWMutex
	ruta  string
	datos archivo
}

func AbrirAlmacen(dir string) (*Almacen, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("no pude preparar %s: %w", dir, err)
	}
	a := &Almacen{ruta: filepath.Join(dir, "usuarios.json")}
	crudo, err := os.ReadFile(a.ruta)
	if errors.Is(err, os.ErrNotExist) {
		a.datos = archivo{Version: 1}
		return a, a.guardar()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(crudo, &a.datos); err != nil {
		return nil, fmt.Errorf("%s ilegible: %w", a.ruta, err)
	}
	if a.datos.Version == 0 {
		a.datos.Version = 1
	}
	return a, nil
}

func (a *Almacen) guardar() error {
	crudo, err := json.MarshalIndent(a.datos, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.ruta + ".tmp"
	if err := os.WriteFile(tmp, append(crudo, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.ruta)
}

func (a *Almacen) Lista() []*Usuario {
	a.mu.RLock()
	defer a.mu.RUnlock()
	copia := make([]*Usuario, len(a.datos.Usuarios))
	for i, u := range a.datos.Usuarios {
		c := *u
		copia[i] = &c
	}
	return copia
}

func (a *Almacen) Vacio() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.datos.Usuarios) == 0
}

func (a *Almacen) buscarLocked(id string) *Usuario {
	for _, u := range a.datos.Usuarios {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func (a *Almacen) PorID(id string) (*Usuario, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if u := a.buscarLocked(id); u != nil {
		c := *u
		return &c, true
	}
	return nil, false
}

func (a *Almacen) PorUsuario(nombre string) (*Usuario, bool) {
	nombre = strings.ToLower(strings.TrimSpace(nombre))
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, u := range a.datos.Usuarios {
		if u.Usuario == nombre {
			c := *u
			return &c, true
		}
	}
	return nil, false
}

func (a *Almacen) adminsActivosLocked(exceptoID string) int {
	n := 0
	for _, u := range a.datos.Usuarios {
		if u.ID != exceptoID && u.Rol == RolAdmin && u.Activo {
			n++
		}
	}
	return n
}

func normalizarUsuario(nombre string) (string, error) {
	nombre = strings.ToLower(strings.TrimSpace(nombre))
	if utf8.RuneCountInString(nombre) < 3 || utf8.RuneCountInString(nombre) > 32 {
		return "", ErrUsuarioInvalido
	}
	for _, r := range nombre {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
		if !ok {
			return "", ErrUsuarioInvalido
		}
	}
	return nombre, nil
}

func validarRol(rol string) error {
	if rol != RolAdmin && rol != RolEditor {
		return ErrRolInvalido
	}
	return nil
}

func validarClave(clave string) error {
	if utf8.RuneCountInString(clave) < 10 {
		return ErrClaveCorta
	}
	return nil
}

func nuevoID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand solo falla si el sistema no tiene entropia
	}
	return "u_" + hex.EncodeToString(b)
}

// Crear agrega una cuenta. Devuelve una copia del usuario creado.
func (a *Almacen) Crear(nombreUsuario, nombre, rol, clave string, activo bool) (*Usuario, error) {
	usuario, err := normalizarUsuario(nombreUsuario)
	if err != nil {
		return nil, err
	}
	if err := validarRol(rol); err != nil {
		return nil, err
	}
	if err := validarClave(clave); err != nil {
		return nil, err
	}
	hash, err := Hashear(clave)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range a.datos.Usuarios {
		if u.Usuario == usuario {
			return nil, ErrUsuarioRepetido
		}
	}
	u := &Usuario{
		ID:      nuevoID(),
		Usuario: usuario,
		Nombre:  strings.TrimSpace(nombre),
		Rol:     rol,
		Activo:  activo,
		Hash:    hash,
		Version: 1,
		Creado:  time.Now().UTC(),
	}
	a.datos.Usuarios = append(a.datos.Usuarios, u)
	if err := a.guardar(); err != nil {
		a.datos.Usuarios = a.datos.Usuarios[:len(a.datos.Usuarios)-1]
		return nil, err
	}
	c := *u
	return &c, nil
}

// Cambios son los campos que el panel puede modificar. Los punteros nil se dejan como estan.
type Cambios struct {
	Nombre *string
	Rol    *string
	Activo *bool
	Clave  *string
}

func (a *Almacen) Actualizar(id string, c Cambios) (*Usuario, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := a.buscarLocked(id)
	if u == nil {
		return nil, ErrNoExiste
	}
	previo := *u

	if c.Nombre != nil {
		u.Nombre = strings.TrimSpace(*c.Nombre)
	}
	if c.Rol != nil {
		if err := validarRol(*c.Rol); err != nil {
			return nil, err
		}
		u.Rol = *c.Rol
	}
	if c.Activo != nil {
		u.Activo = *c.Activo
	}
	if c.Clave != nil {
		if err := validarClave(*c.Clave); err != nil {
			return nil, err
		}
		hash, err := Hashear(*c.Clave)
		if err != nil {
			return nil, err
		}
		u.Hash = hash
	}
	// Desactivar, cambiar el rol o cambiar la clave cierra las sesiones abiertas.
	if (c.Activo != nil && !*c.Activo) || c.Clave != nil || (c.Rol != nil && previo.Rol != u.Rol) {
		u.Version++
	}
	if a.adminsActivosLocked(u.ID) == 0 && !(u.Rol == RolAdmin && u.Activo) {
		*u = previo
		return nil, ErrUltimoAdmin
	}
	if err := a.guardar(); err != nil {
		*u = previo
		return nil, err
	}
	copia := *u
	return &copia, nil
}

func (a *Almacen) Borrar(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, u := range a.datos.Usuarios {
		if u.ID != id {
			continue
		}
		if a.adminsActivosLocked(id) == 0 {
			return ErrUltimoAdmin
		}
		previo := a.datos.Usuarios
		a.datos.Usuarios = append(append([]*Usuario{}, previo[:i]...), previo[i+1:]...)
		if err := a.guardar(); err != nil {
			a.datos.Usuarios = previo
			return err
		}
		return nil
	}
	return ErrNoExiste
}

// MarcarIngreso anota el ultimo acceso correcto. Un error al guardar no impide entrar.
func (a *Almacen) MarcarIngreso(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if u := a.buscarLocked(id); u != nil {
		ahora := time.Now().UTC()
		u.UltimoIngreso = &ahora
		_ = a.guardar()
	}
}

// Hashear devuelve el hash de una contrasena en formato pbkdf2$sha256$iter$salt$dk.
func Hashear(clave string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, clave, salt, iteracionesPBKDF2, 32)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s", iteracionesPBKDF2, b64(salt), b64(dk)), nil
}

// Verificar compara una contrasena con un hash guardado, en tiempo constante.
func Verificar(hash, clave string) bool {
	partes := strings.Split(hash, "$")
	if len(partes) != 5 || partes[0] != "pbkdf2" || partes[1] != "sha256" {
		return false
	}
	iter, err := strconv.Atoi(partes[2])
	if err != nil || iter < 1000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(partes[3])
	if err != nil {
		return false
	}
	esperado, err := base64.RawStdEncoding.DecodeString(partes[4])
	if err != nil {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, clave, salt, iter, len(esperado))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, esperado) == 1
}

// ClaveAlAzar genera una contrasena legible para el primer administrador.
func ClaveAlAzar() string {
	const alfabeto = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	salida := make([]byte, len(b))
	for i, v := range b {
		salida[i] = alfabeto[int(v)%len(alfabeto)]
	}
	return string(salida)
}
