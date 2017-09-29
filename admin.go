package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"

	"github.com/mdigger/log"
	"golang.org/x/crypto/bcrypt"
)

// Admin описывает административный сервер.
type Admin struct {
	config *Config            // конфигурация сервиса
	tmpl   *template.Template // шаблон административного сайта
	proxy  *Proxy             // веб сервер
	mu     sync.RWMutex       // блокировка одновременного доступа к конфигурации
	log    *log.Logger        // для вывода лога
}

// Config отвечает за изменение и отображение конфигурационного файла.
func (a *Admin) Config(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "GET": // отдаем страничку с административным интерфейсом
		var buf bytes.Buffer
		a.config.mu.RLock()
		err := a.tmpl.Execute(&buf, a.config)
		a.config.mu.RUnlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			a.log.Error("template error", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err = buf.WriteTo(w); err != nil {
			a.log.Error("http response error", err)
		}
	case "POST":
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var changed, mxChanged, serverChanged bool
		a.config.mu.Lock()
		for name, values := range r.PostForm {
			if len(values) == 0 {
				continue
			}
			value := strings.TrimSpace(values[0])
			if value == "" {
				continue
			}
			switch name {
			case "admin.login":
				if value == a.config.Admin.Login {
					continue
				}
				a.config.Admin.Login = value
			case "admin.password":
				if bcrypt.CompareHashAndPassword(
					a.config.Admin.Password, []byte(value)) == nil {
					continue
				}
				password, err := bcrypt.GenerateFromPassword(
					[]byte(value), bcrypt.DefaultCost)
				if err != nil {
					a.log.Error("bcrypt password error", err)
					continue
				}
				a.config.Admin.Password = password
			case "server.host":
				if value == a.config.Server.Host {
					continue
				}
				a.config.Server.Host = value
				serverChanged = true
			case "server.log":
				switch value {
				case "ALL":
					log.SetLevel(log.TRACE)
					a.config.Server.LogLevel = -1
				case "INFO":
					log.SetLevel(log.INFO)
					a.config.Server.LogLevel = 0
				case "ERROR":
					log.SetLevel(log.WARN)
					a.config.Server.LogLevel = 1
				default:
					continue
				}
			case "mx.host":
				if value == a.config.MX.Host {
					continue
				}
				a.config.MX.Host = value
				mxChanged = true
			case "mx.login":
				if value == a.config.MX.Login {
					continue
				}
				a.config.MX.Login = value
				mxChanged = true
			case "mx.password":
				if value == string(a.config.MX.Password) {
					continue
				}
				a.config.MX.Password = []byte(value)
				mxChanged = true
			default:
				continue
			}
			changed = true
		}
		a.config.mu.Unlock()
		if changed {
			if err := a.config.Save(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				a.log.Error("config save error", err)
				return
			}
			a.log.Info("config changed")
		}
		if serverChanged || mxChanged {
			a.mu.Lock()
			if a.proxy != nil {
				a.proxy.Close()
			}
			proxy, err := NewProxy(a.config)
			a.config.mu.Lock()
			a.proxy, a.config.err = proxy, err
			a.config.mu.Unlock()
			a.mu.Unlock()
		}
		// после изменения конфигурации перенаправляем на начальную страницу,
		// чтобы сбросить кеш браузера
		http.Redirect(w, r, "/", http.StatusFound)
	default:
		w.Header().Set("Allow", "GET, POST")
		status := http.StatusMethodNotAllowed
		http.Error(w, http.StatusText(status), status)
	}
}

func badAuthorization(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf("Basic realm=\"%s Admin\"", appName))
	status := http.StatusUnauthorized
	http.Error(w, http.StatusText(status), status)
}

// Authorization проверяет авторизацию. Возвращает ошибку, если авторизация
// не прошла.
func (a *Admin) Authorization(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login, password, ok := r.BasicAuth()
		if !ok {
			badAuthorization(w)
			a.log.Error("no authorized request")
			return
		}
		a.mu.RLock()
		if login != a.config.Admin.Login {
			a.mu.RUnlock()
			badAuthorization(w)
			a.log.Error("bad authorization request", "login", login)
			return
		}
		if err := bcrypt.CompareHashAndPassword(
			a.config.Admin.Password, []byte(password)); err != nil {
			a.mu.RUnlock()
			badAuthorization(w)
			a.log.Error("bad authorization password")
			return
		}
		a.mu.RUnlock()
		h.ServeHTTP(w, r) // обрабатываем запрос после авторизации
	}
}

var srcManifestURL = []byte("https://mxflex.connector73.net")

// Manifest отдает файл с манифестом.
func (a *Admin) Manifest(w http.ResponseWriter, r *http.Request) {
	zr, err := zip.OpenReader(manifestName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		a.log.Error("manifest read error", err)
		return
	}
	defer zr.Close()

	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			a.log.Error("manifest file error", err)
			return
		}
		zf, err := zw.Create(f.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			a.log.Error("manifest create file error", err)
			rc.Close()
			return
		}
		if f.Name == "zat/manifest.json" {
			data, err := ioutil.ReadAll(rc)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				a.log.Error("manifest read file error", err)
				rc.Close()
				return
			}
			data = bytes.Replace(data, srcManifestURL, []byte(a.config.ServerURL()), -1)
			if _, err = zf.Write(data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				a.log.Error("manifest write error", err)
				rc.Close()
				return
			}
		} else {
			_, err = io.Copy(zf, rc)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				a.log.Error("manifest write file error", err)
				rc.Close()
				return
			}
		}
		rc.Close()
	}
	if err = zw.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		a.log.Error("manifest close error", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	// w.Header().Set("Content-Disposition", "attachment; filename=manifest.zip")
	if _, err = buf.WriteTo(w); err != nil {
		a.log.Error("manifest http write error")
	} else {
		a.log.Info("manifest generated")
	}
}
