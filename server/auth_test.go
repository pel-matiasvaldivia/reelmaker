package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHashearYVerificar(t *testing.T) {
	hash, err := Hashear("contrasena-larga")
	if err != nil {
		t.Fatal(err)
	}
	if !Verificar(hash, "contrasena-larga") {
		t.Error("la contrasena correcta tendria que validar")
	}
	if Verificar(hash, "contrasena-largA") {
		t.Error("una contrasena distinta no tendria que validar")
	}
	if Verificar("formato-raro", "contrasena-larga") {
		t.Error("un hash con formato invalido no tendria que validar")
	}
}

func TestCookieFirmada(t *testing.T) {
	s := &Sesiones{clave: []byte("clave-de-prueba-con-largo-suficiente")}
	u := &Usuario{ID: "u_1", Version: 3}
	valor, vence := s.Emitir(u)
	if time.Until(vence) < 24*time.Hour {
		t.Error("la sesion tendria que durar varios dias")
	}
	id, version, ok := s.Leer(valor)
	if !ok || id != "u_1" || version != 3 {
		t.Fatalf("lectura incorrecta: %q %d %v", id, version, ok)
	}
	if _, _, ok := s.Leer(valor[:len(valor)-2] + "xx"); ok {
		t.Error("una firma alterada no tendria que validar")
	}
	otra := &Sesiones{clave: []byte("otra-clave-distinta-igual-de-larga")}
	if _, _, ok := otra.Leer(valor); ok {
		t.Error("una cookie firmada con otra clave no tendria que validar")
	}
}

func TestAlmacenReglas(t *testing.T) {
	alm, err := AbrirAlmacen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := alm.Crear("jefa", "Jefa", RolAdmin, "clave-larguisima", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alm.Crear("JEFA", "", RolEditor, "clave-larguisima", true); err != ErrUsuarioRepetido {
		t.Errorf("el nombre repetido (aun con otra capitalizacion) tendria que fallar, dio %v", err)
	}
	if _, err := alm.Crear("nueva", "", RolEditor, "corta", true); err != ErrClaveCorta {
		t.Errorf("una clave corta tendria que fallar, dio %v", err)
	}
	if _, err := alm.Crear("a b", "", RolEditor, "clave-larguisima", true); err != ErrUsuarioInvalido {
		t.Errorf("un usuario con espacios tendria que fallar, dio %v", err)
	}

	no := false
	if _, err := alm.Actualizar(admin.ID, Cambios{Activo: &no}); err != ErrUltimoAdmin {
		t.Errorf("deshabilitar al unico admin tendria que fallar, dio %v", err)
	}
	if err := alm.Borrar(admin.ID); err != ErrUltimoAdmin {
		t.Errorf("borrar al unico admin tendria que fallar, dio %v", err)
	}

	editor, err := alm.Crear("martina", "Martina", RolEditor, "clave-larguisima", true)
	if err != nil {
		t.Fatal(err)
	}
	actualizado, err := alm.Actualizar(editor.ID, Cambios{Activo: &no})
	if err != nil {
		t.Fatal(err)
	}
	if actualizado.Activo {
		t.Error("la cuenta tendria que quedar deshabilitada")
	}
	if actualizado.Version <= editor.Version {
		t.Error("deshabilitar tendria que subir la version para cortar las sesiones")
	}

	// El almacen se relee del disco igual que al reiniciar el contenedor.
	otro, err := AbrirAlmacen(filepath.Dir(alm.ruta))
	if err != nil {
		t.Fatal(err)
	}
	if len(otro.Lista()) != 2 {
		t.Errorf("esperaba 2 cuentas persistidas, hay %d", len(otro.Lista()))
	}
}

// entorno arma un servidor completo con un sitio minimo y un admin conocido.
func entorno(t *testing.T) (*httptest.Server, *Almacen) {
	t.Helper()
	sitio := t.TempDir()
	for _, n := range []string{"index.html", "login.html", "admin.html"} {
		if err := os.WriteFile(filepath.Join(sitio, n), []byte("<p>"+n+"</p>"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	datos := t.TempDir()
	alm, err := AbrirAlmacen(datos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alm.Crear("jefa", "Jefa", RolAdmin, "clave-larguisima", true); err != nil {
		t.Fatal(err)
	}
	ses, err := CargarClave(datos)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{usuarios: alm, sesiones: ses, freno: NuevoFreno(8, time.Minute), estaticos: servirEstaticos(sitio), raiz: sitio}
	srv := httptest.NewServer(rutas(app))
	t.Cleanup(srv.Close)
	return srv, alm
}

func cliente(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{
		Jar: nil,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func pedir(t *testing.T, c *http.Client, metodo, url, cookie, cuerpo string) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if cuerpo == "" {
		r, err = http.NewRequest(metodo, url, nil)
	} else {
		r, err = http.NewRequest(metodo, url, strings.NewReader(cuerpo))
		r.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Reelmaker", "1")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func cookieDe(resp *http.Response) string {
	for _, ck := range resp.Cookies() {
		if ck.Name == nombreCookie && ck.Value != "" {
			return ck.Name + "=" + ck.Value
		}
	}
	return ""
}

func TestFlujoCompleto(t *testing.T) {
	srv, alm := entorno(t)
	c := cliente(t)

	// Sin sesion, la app redirige al ingreso.
	resp := pedir(t, c, "GET", srv.URL+"/", "", "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("esperaba redireccion al login, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// La pagina de ingreso es publica.
	resp = pedir(t, c, "GET", srv.URL+"/login", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("el login tendria que ser publico, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Clave equivocada.
	resp = pedir(t, c, "POST", srv.URL+"/api/login", "", `{"usuario":"jefa","clave":"incorrecta"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("esperaba 401 con clave incorrecta, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Ingreso correcto.
	resp = pedir(t, c, "POST", srv.URL+"/api/login", "", `{"usuario":"jefa","clave":"clave-larguisima"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("esperaba 200 al ingresar, dio %d", resp.StatusCode)
	}
	ck := cookieDe(resp)
	resp.Body.Close()
	if ck == "" {
		t.Fatal("el ingreso tendria que dejar la cookie de sesion")
	}

	// Con la cookie, la app responde.
	resp = pedir(t, c, "GET", srv.URL+"/", ck, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("esperaba 200 en la app, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Un POST sin la cabecera propia se rechaza.
	r, _ := http.NewRequest("POST", srv.URL+"/api/logout", nil)
	r.Header.Set("Cookie", ck)
	sinCabecera, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if sinCabecera.StatusCode != http.StatusForbidden {
		t.Errorf("esperaba 403 sin la cabecera X-Reelmaker, dio %d", sinCabecera.StatusCode)
	}
	sinCabecera.Body.Close()

	// La admin crea una cuenta de editor.
	resp = pedir(t, c, "POST", srv.URL+"/api/admin/usuarios", ck,
		`{"usuario":"martina","nombre":"Martina","rol":"editor","clave":"otra-clave-larga"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("esperaba 201 al crear la cuenta, dio %d", resp.StatusCode)
	}
	var creado struct {
		Usuario map[string]any `json:"usuario"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&creado); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	idEditor, _ := creado.Usuario["id"].(string)
	if idEditor == "" {
		t.Fatal("la respuesta tendria que traer el id de la cuenta creada")
	}

	// El editor entra y usa la herramienta.
	resp = pedir(t, c, "POST", srv.URL+"/api/login", "", `{"usuario":"martina","clave":"otra-clave-larga"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("el editor tendria que poder entrar, dio %d", resp.StatusCode)
	}
	ckEditor := cookieDe(resp)
	resp.Body.Close()

	resp = pedir(t, c, "GET", srv.URL+"/", ckEditor, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("el editor tendria que ver la app, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Pero no el panel ni su API.
	resp = pedir(t, c, "GET", srv.URL+"/api/admin/usuarios", ckEditor, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("el editor no tendria que leer las cuentas, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// La admin lo deshabilita y su sesion deja de valer en el pedido siguiente.
	resp = pedir(t, c, "PATCH", srv.URL+"/api/admin/usuarios/"+idEditor, ck, `{"activo":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("esperaba 200 al deshabilitar, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = pedir(t, c, "GET", srv.URL+"/", ckEditor, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("la sesion de una cuenta deshabilitada tendria que caer, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = pedir(t, c, "POST", srv.URL+"/api/login", "", `{"usuario":"martina","clave":"otra-clave-larga"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("una cuenta deshabilitada no tendria que poder entrar, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Y al habilitarla de nuevo vuelve a entrar.
	resp = pedir(t, c, "PATCH", srv.URL+"/api/admin/usuarios/"+idEditor, ck, `{"activo":true}`)
	resp.Body.Close()
	resp = pedir(t, c, "POST", srv.URL+"/api/login", "", `{"usuario":"martina","clave":"otra-clave-larga"}`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("la cuenta rehabilitada tendria que entrar, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// La admin no puede deshabilitarse a si misma.
	jefa, _ := alm.PorUsuario("jefa")
	resp = pedir(t, c, "PATCH", srv.URL+"/api/admin/usuarios/"+jefa.ID, ck, `{"activo":false}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("esperaba 409 al intentar deshabilitarse, dio %d", resp.StatusCode)
	}
	resp.Body.Close()

	// El healthcheck sigue siendo publico.
	resp = pedir(t, c, "GET", srv.URL+"/healthz", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz tendria que ser publico, dio %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFrenoDeIntentos(t *testing.T) {
	srv, _ := entorno(t)
	c := cliente(t)
	for i := 0; i < 8; i++ {
		resp := pedir(t, c, "POST", srv.URL+"/api/login", "", `{"usuario":"jefa","clave":"mal"}`)
		resp.Body.Close()
	}
	resp := pedir(t, c, "POST", srv.URL+"/api/login", "", `{"usuario":"jefa","clave":"clave-larguisima"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("esperaba 429 despues de 8 intentos fallidos, dio %d", resp.StatusCode)
	}
}

func TestSembrarAdminUnaSolaVez(t *testing.T) {
	alm, err := AbrirAlmacen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := SembrarAdmin(alm, "jefa", "clave-larguisima"); err != nil {
		t.Fatal(err)
	}
	if err := SembrarAdmin(alm, "otra", "clave-larguisima"); err != nil {
		t.Fatal(err)
	}
	if n := len(alm.Lista()); n != 1 {
		t.Errorf("la siembra tendria que correr solo con el almacen vacio, hay %d cuentas", n)
	}
}
